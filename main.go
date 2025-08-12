package main

import (
    "strings"
    "errors"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
    "net/url"
	"os"
    "os/exec"
    "path/filepath"
    "io/fs"
    "slices"
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

func get_or_cache(url string, cache_name string, folder string) ([]byte, error) {
    path := "./.jice/" + folder + "/";
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
        // fmt.Println("404: " + url)
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
            _, ok := props[prop]
            if !ok {
                panic("empty prop: " + prop)
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

func get_dep(config JiceConfig, repo string, g string, a string, v string) Project {
    _, pom := get_thing(strings.Replace(g, ".", "/", -1), a, v);

    thing := fmt.Sprintf("%s-%s-%s.pom", url.QueryEscape(g), url.QueryEscape(a), url.QueryEscape(v));
    var pom_content, err = get_or_cache(repo + "/" + pom, thing, "cache");
    if err != nil {
        pom_content, err = get_or_cache(config.Package.DefaultRepo + "/" + pom, thing, "cache");
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

        parent = get_dep(config, repo, project.Parent.GroupId, project.Parent.ArtifactId, project.Parent.Version);
        for k, v := range parent.Properties {
            props[k] = v;
        }

        if project.GroupId == nil {
            project.GroupId = parent.GroupId
        }
        if project.Version == nil {
            props["project.version"] = *parent.Version;
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

    _, ok := props["project.version"];
    if !ok {
        props["project.version"] = *project.Version;
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

func get_all(config JiceConfig, repo string, g string, a string, v string) Dependency {
    var real Dependency;
    dep := get_dep(config, repo, g, a, v);
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
            } else if *d.Scope == "compile" || *d.Scope == "runtime" {

            } else {
                panic("Unexpected scope: " + *d.Scope)
            }
        }
        deps = append(deps, get_all(config, repo, d.GroupId, d.ArtifactId, *d.Version));
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
        JavacArgs *[]string `toml:"javac_args,omitempty"`
        DefaultRepo string `toml:"default_repo,omitempty"`
    } `toml:"package"`
    Dependencies map[string]string `toml:"dependencies"`
    Repos map[string]string `toml:"repos"`
    Extra map[string]string `toml:"extra`
    Mapping *struct {
        Map string `toml:"map"`
        // Intermediary string `toml:"intermediary"`
        Mapped []string `toml:"mapped"`
    } `toml:"mapping,omitempty"`
}

func do_map(config JiceConfig, deps []Dependency, already_did []string) {
    for _, d := range deps {
        should_map := false;
        for _, r := range config.Mapping.Mapped {
            if strings.HasSuffix(r, "/") {
                r = r[:len(r) - 1];
            }
            if strings.HasSuffix(d.Repo, "/") {
                d.Repo = d.Repo[:len(d.Repo) - 1];
            }
            if d.Repo == r {
                should_map = true;
                break
            }
        }
        if should_map {
            path := fmt.Sprintf(
                "%s-%s-%s.jar",
                url.QueryEscape(d.Group),
                url.QueryEscape(d.Artifact),
                url.QueryEscape(d.Version),
            );
            if !slices.Contains(already_did, path) {
                exists, err := exists("./.jice/cache/" + path);
                check(err);
                if exists {
                    cmd := exec.Command(
                        "java",
                        "-jar",
                        "./.jice/mapping/remapper.jar",
                        "./.jice/cache/" + path,
                        "./.jice/cache/" + path,
                        "./.jice/mapping/mappings.tiny",
                        "official",
                        "named",
                    );
                    out, err := cmd.Output();
                    fmt.Print("Mapping " + path + ": " + string(out));
                    check(err)
                    already_did = append(already_did, path)
                }
            }
        }
        do_map(config, d.Dependencies, already_did);
    }
}

func build(config JiceConfig, deps []Dependency, thingy GroupArtifactToVersionScope) {
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
            "cache",
        );
        if err != nil {
            fmt.Println("WARNING: Failed to get jar for " + g + " " + a + " " + v);
        }
    }
    
    for k, d := range config.Extra {
        thing := strings.Split(k, ":");
        _, err := get_or_cache(d, thing[1] + ".jar", "cache");
        check(err)
    }

    if config.Mapping != nil {
        _, err := get_or_cache(config.Mapping.Map, "mappings.jar", "mapping");
        check(err);
        // _, err = get_or_cache(config.Mapping.Intermediary, "intermediary.jar", "mapping");
        // check(err);
        check(exec.Command("bash", "-c", "cd ./.jice/mapping && unzip -jo mappings.jar").Run());
        // check(exec.Command("bash", "-c", "cd ./.jice/mapping && unzip -jo intermediary.jar && mv mappings.tiny off2inter.tiny").Run());

        _, err = get_or_cache(
            "https://maven.fabricmc.net/net/fabricmc/tiny-remapper/0.9.0/tiny-remapper-0.9.0-fat.jar",
            "remapper.jar",
            "mapping",
        )
        check(err);

        do_map(config, deps, []string{});
    }

    // java -jar tiny-remapper-0.9.0-fat.jar client.jar client-intermediary.jar intermediary.tiny official intermediary
    // java -jar tiny-remapper-0.9.0-fat.jar client-intermediary.jar ../libs/client-1.21.5-mapped.jar yarn.tiny intermediary named

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
        "-cp", "./.jice/cache/*",
        "-d", "./.jice/output/",
    };

    if config.Package.JavacArgs != nil {
        args = append(args, *config.Package.JavacArgs...);
    }
    
    // TODO: Someone could put command args into filename and fuck shit up
    args = append(args, javas...);

    cmd := exec.Command("javac", args...);
    cmd.Stdout = os.Stdout;
    cmd.Stderr = os.Stderr;

    err = cmd.Run();
    check(err);
}

func get_all_deps_from_config(config JiceConfig) []Dependency {
    var deps []Dependency;
    for k, v := range config.Dependencies {
        repo := config.Package.DefaultRepo;
        r, ok := config.Repos[k];
        if ok {
            repo = r;
        }
        thing := strings.Split(k, ":");
        deps = append(deps, get_all(config, repo, thing[0], thing[1], v));
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
    if config.Package.DefaultRepo == "" {
        config.Package.DefaultRepo = "https://repo1.maven.org/maven2";
    }

    args := os.Args[1:];
    if len(args) == 0 {
        fmt.Println("no args");
        return;
    }
    if args[0] == "build" {
        deps := get_all_deps_from_config(config);
        thingy := make(GroupArtifactToVersionScope);
        thing(thingy, deps);
        build(config, deps, thingy);
        // dep := get_all(jice, "https://repo1.maven.org/maven2", "com.google.guava", "guava", "33.3.1-jre")
        // project := get_dep(jice, "https://repo1.maven.org/maven2", "com.google.errorprone", "error_prone_annotations", "2.28.0")
    } else if args[0] == "clean" {
        os.RemoveAll("./.jice")
    } else if args[0] == "tree" {
        tree(get_all_deps_from_config(config), 0)
    } else {
        fmt.Println("Unknown arg: " + args[0])
    }
    pp.Print()
}