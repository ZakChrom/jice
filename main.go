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
	"github.com/k0kubun/pp/v3"
    "github.com/sblinch/kdl-go"
    "github.com/yuin/gopher-lua"
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
            management[k] = struct { Version string; Scope *string; Repo string; Url *string } {
                Version: replace_stupid_string_with_props(d.Version, props),
                Scope: d.Scope,
                Repo: repo,
                Url: nil,
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
        management[k] = struct { Version string; Scope *string; Repo string; Url *string }{
            Version: replace_stupid_string_with_props(d.Version, props),
            Scope: d.Scope,
            Repo: repo,
            Url: nil,
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
    L *lua.LState
    Package struct {
        SourceDir string `kdl:"source_dir"`
        JavacArgs *[]string `kdl:"javac_args,omitempty"`
        DefaultRepo string `kdl:"default_repo,omitempty"`
    } `kdl:"package"`
    Dependencies map[string]string `kdl:"dependencies"`
    Repos map[string]string `kdl:"repos"`
    Extra map[string]string `kdl:"extra"`
    Plugins map[string]struct {
        Url string `kdl:"url"`
        Config map[string]any `kdl:"config"`
        Table *lua.LTable
    } `kdl:"plugins"`
}

type CacheThing struct {
    Mapped []string `kdl:"mapped"`
}

func build(config JiceConfig, deps []Dependency, thingy GroupArtifactToVersionScope) {
    for k, d := range thingy {
        g := k.GroupId;
        a := k.ArtifactId;
        v := d.Version;

        jar, _ := get_thing(strings.Replace(g, ".", "/", -1), a, v);
        if d.Repo == "from extra deps" {
            _, err := get_or_cache(
                *d.Url,
                fmt.Sprintf(
                    "%s-%s-%s.jar",
                    url.QueryEscape(g),
                    url.QueryEscape(a),
                    url.QueryEscape(v),
                ),
                "cache",
            );
            if err != nil {
                fmt.Println("WARNING: Failed to get extra jar for " + g + " " + a + " " + v + ": " + err.Error());
            }
        } else {
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
                fmt.Println("WARNING: Failed to get jar for " + g + " " + a + " " + v + ": " + err.Error());
            }
        }
    }

    for k, p := range config.Plugins {
        lv := config.L.GetTable(p.Table, lua.LString("before_build"));
        if fun, ok := lv.(*lua.LFunction); ok {
            fmt.Println("Running plugin " + k + " before_build")
            err := config.L.CallByParam(lua.P {
                Fn: fun,
                NRet: 0,
                Protect: true,
            })
            if err != nil {
                panic("lua error in plugin " + k + ": " + err.Error())
            }
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
    for k, d := range config.Extra {
        thing := strings.Split(k, ":");
        desc := "extra dependency"
        deps = append(deps, Dependency {
            Group: thing[0],
            Artifact: thing[1],
            Version: "0.69.420",
            Name: thing[1],
            Description: &desc,
            Url: &d,
            Dependencies: []Dependency{},
            Repo: "from extra deps",
        })
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
    Url *string
}

func thing(thingy GroupArtifactToVersionScope, deps []Dependency) {
    for _, d := range deps {
        k := struct { GroupId string; ArtifactId string }{
            GroupId: d.Group,
            ArtifactId: d.Artifact,
        };
        _, ok := thingy[k];
        if !ok {
            thingy[k] = struct { Version string; Scope *string; Repo string; Url *string } {
                Version: d.Version,
                Scope: nil,
                Repo: d.Repo,
                Url: d.Url,
            }
        }
        thing(thingy, d.Dependencies)
    }
}

func any_to_lua(L *lua.LState, m map[string]any) *lua.LTable {
    tbl := L.NewTable()
    for k, v := range m {
        switch val := v.(type) {
            case string:         tbl.RawSetString(k, lua.LString(val))
            case *string:        tbl.RawSetString(k, lua.LString(*val))
            case int:            tbl.RawSetString(k, lua.LNumber(val))
            case float64:        tbl.RawSetString(k, lua.LNumber(val))
            case bool:           tbl.RawSetString(k, lua.LBool(val))
            case map[string]any: tbl.RawSetString(k, any_to_lua(L, val))
            case nil:            tbl.RawSetString(k, lua.LNil)
            case lua.LValue:     tbl.RawSetString(k, val)
            default:
                pp.Println(val)
                panic("Unknown type")
        }
    }
    return tbl
}

func lua_get_dep(L *lua.LState, deps []Dependency) *lua.LTable {
    tbl := L.NewTable()
    for i, d := range deps {
        thing := make(map[string]any)
        thing["group"] = d.Group
        thing["artifact"] = d.Artifact
        thing["version"] = d.Version
        thing["name"] = d.Group
        if d.Description != nil {
            thing["description"] = d.Group
        }
        if d.Url != nil {
            thing["url"] = d.Url
        }
        thing["repo"] = d.Repo
        thing["dependencies"] = lua_get_dep(L, d.Dependencies)
        tbl.RawSetInt(i + 1, any_to_lua(L, thing))
    }
    return tbl
}

func init_lua(config JiceConfig, deps []Dependency) {
    L := config.L;
    jice_tbl := L.NewTable()
    L.SetFuncs(jice_tbl, map[string]lua.LGFunction{
        "get_or_cache": func(L *lua.LState) int {
            url := L.CheckString(1)
            name := L.CheckString(2)
            folder := L.CheckString(3)
            stuff, err := get_or_cache(url, name, folder)
            if err == nil {
                L.Push(lua.LString(stuff))
                return 1
            } else {
                L.Push(lua.LNil)
                L.Push(lua.LString(err.Error()))
                return 2
            }
        },
        "get_dependencies": func(L *lua.LState) int {
            L.Push(lua_get_dep(L, deps))
            return 1
        },
        "query_escape": func(L *lua.LState) int {
            L.Push(lua.LString(url.QueryEscape(L.CheckString(1))));
            return 1
        },
    })
    L.SetGlobal("Jice", jice_tbl)

    for k, v := range config.Plugins {
        a := any_to_lua(L, v.Config)
        // pp.Println(a)
        L.SetGlobal("config", a)
        plugin, err := get_or_cache(v.Url, k + ".lua", "plugins")
        check(err)
        if err := L.DoString(string(plugin)); err != nil {
            panic(err)
        }
        lv := L.Get(-1);
        if tbl, ok := lv.(*lua.LTable); ok {
            plugin := config.Plugins[k];
            plugin.Table = tbl
            config.Plugins[k] = plugin;
        } else {
            panic("plugin " + k + " didnt return a table")
        }
    }
}

func main() {
    L := lua.NewState(lua.Options { SkipOpenLibs: true })
    defer L.Close()
    for _, pair := range []struct{ n string; f lua.LGFunction } {
		{ lua.BaseLibName,   lua.OpenBase    },
		{ lua.TabLibName,    lua.OpenTable   },
		{ lua.MathLibName,   lua.OpenMath    },
		{ lua.StringLibName, lua.OpenString  },
		{ lua.OsLibName,     lua.OpenOs      },
		{ lua.IoLibName,     lua.OpenIo      },
	} {
		if err := L.CallByParam(lua.P {
			Fn: L.NewFunction(pair.f),
			NRet: 0,
			Protect: true,
		}, lua.LString(pair.n)); err != nil {
			panic(err)
		}
	}

    var config JiceConfig;
    text, err := os.ReadFile("jice.kdl");
    check(err);
    err = kdl.Unmarshal(text, &config)
    check(err);
    
    config.L = L;
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
        init_lua(config, deps)
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