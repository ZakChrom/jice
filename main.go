package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
    "net/url"
	"os"
    "os/exec"
    "path/filepath"
    "io/fs"
    "strings"
    "errors"
	"github.com/k0kubun/pp/v3"
    "github.com/pelletier/go-toml/v2"
)

type XMLProperties map[string]string

func (p *XMLProperties) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = make(map[string]string);
	for {
		tok, err := d.Token();
		if err != nil {
			if err == io.EOF {
				break;
			}
			return err;
		}

		switch se := tok.(type) {
            case xml.StartElement:
                var value string;
                if err := d.DecodeElement(&value, &se); err != nil {
                    return err;
                }
                (*p)[se.Name.Local] = strings.TrimSpace(value);
            case xml.EndElement:
                if se.Name.Local == start.Name.Local {
                    return nil;
                }
        }
	}
	return nil;
}

type Project struct {
    Parent *struct {
        GroupId string `xml:"groupId"`
        ArtifactId string `xml:"artifactId"`
        Version string `xml:"version"`
    } `xml:"parent,omitempty"`
    GroupId *string `xml:"groupId,omitempty"`
    ArtifactId string `xml:"artifactId"`
    Version *string `xml:"version,omitempty"`
    Name string `xml:"name"`
    Url *string `xml:"url,omitempty"`
    Description *string `xml:"description,omitempty"`
    Properties XMLProperties `xml:"properties"`
    Dependencies []struct {
        GroupId string `xml:"groupId"`
        ArtifactId string `xml:"artifactId"`
        Version *string `xml:"version,omitempty"`
        Scope *string `xml:"scope,omitempty"`
    } `xml:"dependencies>dependency"`
    DependencyManagement []struct {
        GroupId string `xml:"groupId"`
        ArtifactId string `xml:"artifactId"`
        Version string `xml:"version"`
        Scope *string `xml:"scope,omitempty"`
    } `xml:"dependencyManagement>dependencies>dependency"`
}

func check(e error) {
    if e != nil {
        panic(e)
    }
}

func get_thing(g string, a string, v string) (string, string) {
    return fmt.Sprintf("%s/%s/%s/%s-%s.jar", g, a, v, a, v), fmt.Sprintf("%s/%s/%s/%s-%s.pom", g, a, v, a, v);
}

// What kind of a language doesnt have a way to check if a file exists in the std library
// Ugh i wish i could use rust but quick_xml doesnt have serde support i think
func exists(path string) (bool, error) {
    _, err := os.Stat(path)
	if os.IsNotExist(err) {
        return false, nil;
	} else if err != nil {
        return false, err;
	} else {
        return true, nil;
	}
}

func get_or_cache(url string, cache_name string, jar bool) ([]byte, error) {
    path := "./.jice/"
    if jar {
        path += "jars/"
    } else {
        path += "poms/"
    }
    err := os.MkdirAll(path, 0775);
    check(err);

    path += cache_name;

    exist, err := exists(path);
    check(err);
    if exist {
        text, err := os.ReadFile(path);
        check(err);
        return text, nil;
    }

    resp, err := http.Get(url);
    check(err);
    defer resp.Body.Close();

    if resp.StatusCode == 404 {
        fmt.Println("404: " + url)
        return nil, errors.New("404")
    }

    text, err := io.ReadAll(resp.Body);
    check(err);
    
    err = os.WriteFile(path, text, 0644);
    check(err);

    return text, nil;
}

type Dependency struct {
    Group string
    Artifact string
    Version string
    Name string
    Description *string
    Url *string
    Dependencies []Dependency
    Repo string
}

func replace_stupid_string_with_props(s string, props map[string]string) string {
    var new string;
    var prop string;
    stupid := 0;
    for _, c := range s {
        if stupid == 0 {
            if c == '$' {
                stupid = 1;
                continue;
            }
            new += string(c);
            continue;
        }
        if stupid == 1 && c != '{' {
            stupid = 0;
            new += string(c);
            continue;
        } else if stupid == 1 && c == '{' {
            stupid = 2;
            continue;
        }
        if c == '}' {
            stupid = 0;
            if props[prop] == "" {
                panic("empty prop")
            }
            new += props[prop];
            prop = "";
        } else {
            prop += string(c);
        }
    }
    if prop != "" {
        panic("prop != \"\"");
    }
    if stupid != 0 {
        panic("stupid != 0");
    }
    return new;
}

func get_dep(jice Jice, repo string, g string, a string, v string) Project {
    _, pom := get_thing(strings.Replace(g, ".", "/", -1), a, v);

    var pom_content, err = get_or_cache(repo + "/" + pom, fmt.Sprintf("%s-%s-%s.pom", url.QueryEscape(g), url.QueryEscape(a), url.QueryEscape(v)), false);
    if err != nil {
        pom_content, err = get_or_cache(jice.DefaultRepo + "/" + pom, fmt.Sprintf("%s-%s-%s.pom", g, a, v), false);
        check(err)
    }
    // fmt.Println(string(pom_content));

    var project Project;
    err = xml.Unmarshal(pom_content, &project);
    check(err);

    props := make(map[string]string);
    management := make(GroupArtifactToVersionScope);
    var parent Project;
    if project.Parent != nil {
        if strings.Contains(project.Parent.GroupId, "${")    { panic("fuck off") }
        if strings.Contains(project.Parent.ArtifactId, "${") { panic("fuck off") }
        if strings.Contains(project.Parent.Version, "${")    { panic("fuck off") }

        parent = get_dep(jice, repo, project.Parent.GroupId, project.Parent.ArtifactId, project.Parent.Version);
        for k, v := range parent.Properties {
            props[k] = v;
        }

        if project.GroupId == nil {
            project.GroupId = parent.GroupId
        }
        if project.Version == nil {
            project.Version = parent.Version
        }
        if project.Url == nil {
            project.Url = parent.Url
        }
        if project.Description == nil {
            project.Description = parent.Description
        }
    }

    for k, v := range project.Properties {
        props[k] = v;
    }

    if project.Parent != nil {
        for _, d := range parent.DependencyManagement {
            k := struct {
                GroupId string
                ArtifactId string
            } {
                GroupId: d.GroupId,
                ArtifactId: d.ArtifactId,
            };
            management[k] = struct { Version string; Scope *string; Repo string } {
                Version: replace_stupid_string_with_props(d.Version, props),
                Scope: d.Scope,
                Repo: repo,
            }
        }
    }

    for _, d := range project.DependencyManagement {
        k := struct {
            GroupId string
            ArtifactId string
        } {
            GroupId: d.GroupId,
            ArtifactId: d.ArtifactId,
        };
        management[k] = struct { Version string; Scope *string; Repo string }{
            Version: replace_stupid_string_with_props(d.Version, props),
            Scope: d.Scope,
            Repo: repo,
        }
    }

    for k, d := range project.Dependencies {
        for k, v := range management {
            if k.GroupId == d.GroupId && k.ArtifactId == d.ArtifactId {
                if d.Scope == nil {
                    d.Scope = v.Scope;
                }
                if d.Version == nil {
                    d.Version = &v.Version;
                }
                break
            }
        }
        project.Dependencies[k] = d;
    }
    
    for k, dep := range project.Dependencies {
        dep.GroupId = replace_stupid_string_with_props(dep.GroupId, props);
        dep.ArtifactId = replace_stupid_string_with_props(dep.ArtifactId, props);
        thing := replace_stupid_string_with_props(*dep.Version, props);
        dep.Version = &thing;
        project.Dependencies[k] = dep;
    }

    return project;
}

func get_all(jice Jice, repo string, g string, a string, v string) Dependency {
    var real Dependency;
    dep := get_dep(jice, repo, g, a, v);
    real.Group = *dep.GroupId;
    real.Artifact = dep.ArtifactId;
    real.Version = *dep.Version;
    real.Name = dep.Name;
    real.Description = dep.Description;
    real.Url = dep.Url;
    real.Repo = repo;
    // pp.Println(real.Group, real.Artifact, real.Version);

    var deps []Dependency;
    for _, d := range dep.Dependencies {
        if d.Scope != nil {
            if *d.Scope == "test" {
                continue
            } else if *d.Scope == "compile" {

            } else {
                panic("a")
            }
        }
        deps = append(deps, get_all(jice, repo, d.GroupId, d.ArtifactId, *d.Version));
    }
    real.Dependencies = deps;
    return real;
}

func tree(deps []Dependency, indent int) {
    for _, d := range deps {
        if indent > 0 {
            for j := 0; j < indent; j++ {
                fmt.Print("  ")
            }
        }
        fmt.Print(d.Group + ":" + d.Artifact + " " + d.Version)
        fmt.Print("\n")
        tree(d.Dependencies, indent + 1);
    }
}

type JiceConfig struct {
    Package struct {
        SourceDir string `toml:"source_dir"`
    } `toml:"package"`
    Dependencies map[string]string `toml:"dependencies"`
    Repos map[string]string `toml:"repos"`
}

type Jice struct {
    DefaultRepo string
}

func build(jice Jice, config JiceConfig, deps []Dependency, thingy GroupArtifactToVersionScope) {
    for k, d := range thingy {
        g := k.GroupId;
        a := k.ArtifactId;
        v := d.Version;

        jar, _ := get_thing(strings.Replace(g, ".", "/", -1), a, v);
        _, err := get_or_cache(
            d.Repo + "/" + jar,
            fmt.Sprintf(
                "%s-%s-%s.jar",
                url.QueryEscape(g),
                url.QueryEscape(a),
                url.QueryEscape(v),
            ),
            true,
        );
        if err != nil {
            fmt.Println("WARNING: Failed to get jar for " + g + " " + a + " " + v);
        }
    }


    os.RemoveAll("./.jice/output")

    var javas []string;
    err := filepath.WalkDir(config.Package.SourceDir, func(path string, d fs.DirEntry, err error) error {
        if strings.HasSuffix(path, ".java") {
            javas = append(javas, path)
        }
        return nil;
    });
    check(err);
    
    args := []string {
        "-proc:none",
        "-cp", "./.jice/jars/*",
        "-d", "./.jice/output/",
    };
    
    // TODO: Someone could put command args into filename and fuck shit up
    args = append(args, javas...);

    cmd := exec.Command("javac", args...);
    cmd.Stdout = os.Stdout;
    cmd.Stderr = os.Stderr;

    err = cmd.Run();
    check(err);
}

func get_all_deps_from_config(jice Jice, config JiceConfig) []Dependency {
    var deps []Dependency;
    for k, v := range config.Dependencies {
        repo := jice.DefaultRepo;
        r, ok := config.Repos[k];
        if ok {
            repo = r;
        }
        thing := strings.Split(k, ":");
        deps = append(deps, get_all(jice, repo, thing[0], thing[1], v));
    }
    return deps;
}

type GroupArtifactToVersionScope map[struct {
    GroupId string
    ArtifactId string
}] struct {
    Version string
    Scope *string
    Repo string
}

func thing(thingy GroupArtifactToVersionScope, deps []Dependency) {
    for _, d := range deps {
        k := struct { GroupId string; ArtifactId string }{
            GroupId: d.Group,
            ArtifactId: d.Artifact,
        };
        _, ok := thingy[k];
        if !ok {
            thingy[k] = struct { Version string; Scope *string; Repo string } {
                Version: d.Version,
                Scope: nil,
                Repo: d.Repo,
            }
        }
        thing(thingy, d.Dependencies)
    }
}

func main() {
    var config JiceConfig;
    text, err := os.ReadFile("jice.toml");
    check(err);
    err = toml.Unmarshal(text, &config)
    check(err);

    var jice Jice;
    jice.DefaultRepo = "https://repo1.maven.org/maven2";

    args := os.Args[1:];
    if len(args) == 0 {
        fmt.Println("no args");
        return;
    }
    if args[0] == "build" {
        deps := get_all_deps_from_config(jice, config);
        thingy := make(GroupArtifactToVersionScope);
        thing(thingy, deps);
        build(jice, config, deps, thingy);
        // dep := get_all(jice, "https://repo1.maven.org/maven2", "com.google.guava", "guava", "33.3.1-jre")
        // project := get_dep(jice, "https://repo1.maven.org/maven2", "com.google.errorprone", "error_prone_annotations", "2.28.0")
    } else if args[0] == "clean" {
        os.RemoveAll("./.jice")
    } else if args[0] == "tree" {
        tree(get_all_deps_from_config(jice, config), 0)
    } else {
        fmt.Println("Unknown arg: " + args[0])
    }
    pp.Print()
}