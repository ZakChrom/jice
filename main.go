package main

import (
	"encoding/xml"
    "encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	// "strconv"
	"strings"

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
    Scope *string
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
    management := make(GroupArtifactToDep);
    var parent Project;
    if project.Parent != nil {
        if strings.Contains(project.Parent.GroupId, "${")    { panic("fuck off") }
        if strings.Contains(project.Parent.ArtifactId, "${") { panic("fuck off") }
        if strings.Contains(project.Parent.Version, "${")    { panic("fuck off") }

        parent = get_dep(config, repo, project.Parent.GroupId, project.Parent.ArtifactId, project.Parent.Version);
        maps.Copy(props, parent.Properties)

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
            management[k] = Dependency{
                Group: d.GroupId,
                Artifact: d.ArtifactId,
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
        management[k] = Dependency{
            Group: d.GroupId,
            Artifact: d.ArtifactId,
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

func tree(deps []Dependency, indent int, prefix string) {
    for i, d := range deps {
        last := i == len(deps) - 1

        thing := "├── "
        if last {
            thing = "└── "
        }
        fmt.Println(prefix + thing + "\x1b[38;2;140;140;140m" + d.Group + " \x1b[0m" + d.Artifact + " \x1b[38;2;184;168;255m" + d.Version + "\x1b[0m")
        new := prefix
        if last {
            new += "   "
        } else {
            new += "│  "
        }
        tree(d.Dependencies, indent + 1, new);
    }
}

type JiceConfig struct {
    L *lua.LState
    Package struct {
        SourceDir []string `kdl:"source_dir"`
        JavacArgs *[]string `kdl:"javac_args,omitempty"`
        DefaultRepo string `kdl:"default_repo,omitempty"`
        Resources *[]string `kdl:"resources,omitempty"`
        Main *string `kdl:"main,omitempty"`
        Classpath *[]string `kdl:"classpath,omitempty"`
    } `kdl:"package"`
    Dependencies map[string]string `kdl:"dependencies"`
    Repos map[string]string `kdl:"repos"`
    Extra map[string]string `kdl:"extra"`
    Plugins map[string]struct {
        Url string `kdl:"url"`
        Config map[string]any `kdl:"config"`
        Table *lua.LTable
    } `kdl:"plugins"`
    Variants map[string]JiceConfig `kdl:"variants"`
}

type CacheThing struct {
    Mapped []string `kdl:"mapped"`
}

func get_source_files(config JiceConfig) ([]string, error) {
    var javas []string;
    for i := range config.Package.SourceDir {
        err := filepath.WalkDir(config.Package.SourceDir[i], func(path string, d fs.DirEntry, err error) error {
            if strings.HasSuffix(path, ".java") {
                javas = append(javas, "./" + path)
            }
            return nil;
        });
        if err != nil {
            return []string{}, nil
        }
    }
    return javas, nil
}

func build(config JiceConfig, thingy GroupArtifactToDep) {
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
    os.Remove("./.jice/build.jar")

    javas, err := get_source_files(config);
    check(err);
    
    classpath := "./.jice/cache/*"
    if config.Package.Classpath != nil {
        for _, v := range *config.Package.Classpath {
            classpath += ":" + v
        }
    }

    args := []string {
        "-cp", classpath,
        "-d", "./.jice/output/",
    };

    if config.Package.JavacArgs != nil {
        args = append(args, *config.Package.JavacArgs...);
    }
    
    // TODO: Someone could put command args into filename and fuck shit up
    // EDIT: Changed to have ./ infront of the files. Perhaps thats enough
    args = append(args, javas...);

    for k, p := range config.Plugins {
        lv := config.L.GetTable(p.Table, lua.LString("javac_args"));
        if fun, ok := lv.(*lua.LFunction); ok {
            fmt.Println("Running plugin " + k + " javac_args")
            err := config.L.CallByParam(lua.P {
                Fn: fun,
                NRet: 1,
                Protect: true,
            })
            if err != nil {
                panic("lua error in plugin " + k + ": " + err.Error())
            }
            a := config.L.Get(-1)
            if a != nil {
                if a.Type() == lua.LTTable {
                    config.L.ForEach(a.(*lua.LTable), func(_, v lua.LValue) {
                        if v.Type() == lua.LTString {
                            args = append(args, v.String())
                        }
                    })
                }
            }
        }
    }

    cmd := exec.Command("javac", args...);
    cmd.Stdout = os.Stdout;
    cmd.Stderr = os.Stderr;

    err = cmd.Run();
    check(err);

    args = []string{
        "cvf",
        "./.jice/build.jar",
        "-C",
        "./.jice/output/",
        ".",
    };

    if config.Package.Resources != nil {
        for _, d := range(*config.Package.Resources) {
            d, err = filepath.Abs(d)
            check(err);
            if d == "/" || d == "." || d == "" || d == "./" {
                panic("no")
            }
            args = append(args, "-C", d, ".")
        }
    }

    cmd = exec.Command("jar", args...)
    cmd.Stderr = os.Stderr;
    err = cmd.Run()
    check(err);

    if config.Package.Main != nil {
        f, err := os.CreateTemp("/tmp", "jice_manifest_*")
        check(err)

        _, err = f.WriteString("Main-Class: " + *config.Package.Main + "\n")
        check(err)

        args = []string{
            "ufm",
            "./.jice/build.jar",
            f.Name(),
        }

        cmd = exec.Command("jar", args...)
        cmd.Stderr = os.Stderr
        err = cmd.Run()
        check(err)
    }

    for k, p := range config.Plugins {
        lv := config.L.GetTable(p.Table, lua.LString("after_build"));
        if fun, ok := lv.(*lua.LFunction); ok {
            fmt.Println("Running plugin " + k + " after_build")
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

type GroupArtifactToDep map[struct {
    GroupId string
    ArtifactId string
}] Dependency

func make_ga2dep(thingy GroupArtifactToDep, deps []Dependency) {
    for _, d := range deps {
        k := struct { GroupId string; ArtifactId string }{
            GroupId: d.Group,
            ArtifactId: d.Artifact,
        };
        _, ok := thingy[k];
        if !ok {
            thingy[k] = d
        }
        make_ga2dep(thingy, d.Dependencies)
    }   
}

func map_value_to_any[K comparable, V any](m map[K]V) map[K]any {
    out := make(map[K]any, len(m))
    for k, v := range m {
        out[k] = v
    }
    return out
}

func any_to_lua2(L *lua.LState, m map[float64]any) *lua.LTable {
    tbl := L.CreateTable(len(m), 0)
    for k, v := range m {
        k2 := int(k)
        switch val := v.(type) {
            case string:         {
                // f, err := strconv.ParseFloat(val, 64)
                // if err != nil {
                    tbl.RawSetInt(k2, lua.LString(val))
                // } else {
                    // tbl.RawSetInt(k2, lua.LNumber(f))
                // }
            }
            case *string:            tbl.RawSetInt(k2, lua.LString(*val))
            case int:                tbl.RawSetInt(k2, lua.LNumber(val))
            case float64:            tbl.RawSetInt(k2, lua.LNumber(val))
            case bool:               tbl.RawSetInt(k2, lua.LBool(val))
            case map[string]string:  tbl.RawSetInt(k2, any_to_lua(L, map_value_to_any(val)))
            case map[string]int:     tbl.RawSetInt(k2, any_to_lua(L, map_value_to_any(val)))
            case map[string]float64: tbl.RawSetInt(k2, any_to_lua(L, map_value_to_any(val)))
            case map[string]any:     tbl.RawSetInt(k2, any_to_lua(L, val))
            case nil:                tbl.RawSetInt(k2, lua.LNil)
            case lua.LValue:         tbl.RawSetInt(k2, val)
            case map[float64]string: tbl.RawSetInt(k2, any_to_lua2(L, map_value_to_any(val)))
            case map[float64]int:    tbl.RawSetInt(k2, any_to_lua2(L, map_value_to_any(val)))
            case map[float64]float64:tbl.RawSetInt(k2, any_to_lua2(L, map_value_to_any(val)))
            case map[float64]any:    tbl.RawSetInt(k2, any_to_lua2(L, val))
            default:
                pp.Println(val)
                panic("Unknown type")
        }
    }
    return tbl
}

func any_to_lua(L *lua.LState, m map[string]any) *lua.LTable {
    tbl := L.NewTable()
    for k, v := range m {
        switch val := v.(type) {
            case string:         {
                // TODO: In kdl or atleast in this go implementation arrays arent a thing
                // So kdl parser converts indexes to strings so i convert back
                // Commented because json has an actually good array support which fucks up here

                // f, err := strconv.ParseFloat(val, 64)
                // if err != nil {
                    tbl.RawSetString(k, lua.LString(val))
                // } else {
                    // tbl.RawSetString(k, lua.LNumber(f))
                // }
            }
            case *string:            tbl.RawSetString(k, lua.LString(*val))
            case int:                tbl.RawSetString(k, lua.LNumber(val))
            case float64:            tbl.RawSetString(k, lua.LNumber(val))
            case bool:               tbl.RawSetString(k, lua.LBool(val))
            case map[string]string:  tbl.RawSetString(k, any_to_lua(L, map_value_to_any(val)))
            case map[string]int:     tbl.RawSetString(k, any_to_lua(L, map_value_to_any(val)))
            case map[string]float64: tbl.RawSetString(k, any_to_lua(L, map_value_to_any(val)))
            case map[string]any:     tbl.RawSetString(k, any_to_lua(L, val))
            case nil:                tbl.RawSetString(k, lua.LNil)
            case lua.LValue:         tbl.RawSetString(k, val)
            case map[float64]string: tbl.RawSetString(k, any_to_lua2(L, map_value_to_any(val)))
            case map[float64]int:    tbl.RawSetString(k, any_to_lua2(L, map_value_to_any(val)))
            case map[float64]float64:tbl.RawSetString(k, any_to_lua2(L, map_value_to_any(val)))
            case map[float64]any:    tbl.RawSetString(k, any_to_lua2(L, val))
            case []any:
                thing := make(map[float64]any)
                for i, v := range val {
                    thing[float64(i)] = v
                }
                tbl.RawSetString(k, any_to_lua2(L, thing))
            default:
                pp.Println(val)
                panic("Unknown type")
        }
    }
    return tbl
}

func lua_to_any(v lua.LValue) any {
    if v.Type() == lua.LTBool {
        return bool(lua.LVAsBool(v))
    }
    if v.Type() == lua.LTNil {
        return nil
    }
    if v.Type() == lua.LTNumber {
        return float64(lua.LVAsNumber(v))
    }
    if v.Type() == lua.LTString {
        return string(lua.LVAsString(v))
    }
    if v.Type() == lua.LTTable {
        t, ok := v.(*lua.LTable)
        if !ok { panic("") }

        if t.MaxN() > 0 {
            m := map[int]any{};
            t.ForEach(func(key lua.LValue, value lua.LValue) {
                v := lua_to_any(value)
                m[int(lua.LVAsNumber(key))] = v;
            })
            return m;
        } else {
            m := map[string]any{};
            t.ForEach(func(key lua.LValue, value lua.LValue) {
                v := lua_to_any(value)
                m[lua.LVAsString(key)] = v;
            })
            return m;
        }
    }

    pp.Println(v.Type())
    panic("Unknown type")
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

func cppy_file(src string, dst string) {
    sourceFileStat, err := os.Stat(src)
    check(err);

    if !sourceFileStat.Mode().IsRegular() {
        panic(fmt.Errorf("%s is not a regular file", src))
    }

    source, err := os.Open(src)
    check(err);
    defer source.Close()

    destination, err := os.Create(dst)
    check(err);
    defer destination.Close()

    _, err = io.Copy(destination, source)
    check(err);
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
        // "add_dependency": func(l *lua.LState) int {
        //     tbl := L.CheckTable(1)
            
        //     group := tbl.RawGetString("group")
        //     if group == lua.LNil {
        //         L.ArgError(1, "group field is nil")
        //     }
            
        //     artifact := tbl.RawGetString("artifact")
        //     if artifact == lua.LNil {
        //         L.ArgError(1, "artifact field is nil")
        //     }
            
        //     version := tbl.RawGetString("version")
        //     if version == lua.LNil {
        //         L.ArgError(1, "version field is nil")
        //     }
            
        //     name := tbl.RawGetString("name")
        //     if name == lua.LNil {
        //         L.ArgError(1, "name field is nil")
        //     }

        //     description := tbl.RawGetString("description")
        //     var description_real *string = nil
        //     if description.Type() == lua.LTString {
        //         v := lua.LVAsString(description)
        //         description_real = &v
        //     }
        //     url := tbl.RawGetString("url")
        //     var url_real *string = nil
        //     if url.Type() == lua.LTString {
        //         v := lua.LVAsString(url)
        //         url_real = &v
        //     }
            
        //     repo := tbl.RawGetString("repo")
        //     if repo == lua.LNil {
        //         L.ArgError(1, "repo field is nil")
        //     }
        //     scope := "compile"

        //     *deps = append(*deps, Dependency{
        //         Group: lua.LVAsString(group),
        //         Artifact: lua.LVAsString(artifact),
        //         Version: lua.LVAsString(version),
        //         Name: lua.LVAsString(name),
        //         Description: description_real,
        //         Url: url_real,
        //         Dependencies: []Dependency{},
        //         Repo: lua.LVAsString(repo),
        //         Scope: &scope,
        //     })
        //     return 0;
        // },
        "write_json": func(L *lua.LState) int {
            file := L.CheckString(1)
            thing := lua_to_any(L.CheckAny(2))
            f, err := os.OpenFile(file, os.O_WRONLY | os.O_CREATE | os.O_TRUNC, 0666)
            check(err)

            thing2, ok := thing.(map[string]any)
            if !ok { panic("thing2") }

            kdl, err := json.Marshal(thing2)
            check(err)

            _, err = f.Write(kdl)
            check(err)

            check(f.Close())
            return 0;
        },
        // "read_kdl": func(l *lua.LState) int {
        //     file := L.CheckString(1)
        //     f, err := os.Open(file)
        //     check(err)

        //     stat, err := f.Stat()
        //     check(err)

        //     data := make([]byte, stat.Size())
        //     num, err := f.Read(data)
        //     check(err)

        //     var value map[string]map[float64]string;
        //     check(kdl.Unmarshal(data[:num], &value))

        //     stupid := make(map[string]any)
        //     for k, v := range value {
        //         stupid[k] = v
        //     }

        //     tbl := any_to_lua(L, stupid)

        //     L.Push(tbl)
        //     return 1;
        // },
        "read_json": func(l *lua.LState) int {
            file := L.CheckString(1)
            f, err := os.Open(file)
            check(err)

            stat, err := f.Stat()
            check(err)

            data := make([]byte, stat.Size())
            num, err := f.Read(data)
            check(err)

            var value map[string]any;
            check(json.Unmarshal(data[:num], &value))

            tbl := any_to_lua(L, value)

            L.Push(tbl)
            return 1;
        },
        // Dumbass xml cant unmarshal into map[string]any so specialize it for now ig
        "get_yarn_metadata_xml_versions": func(l *lua.LState) int {
            file := L.CheckString(1)
            f, err := os.Open(file)
            check(err)

            stat, err := f.Stat()
            check(err)

            data := make([]byte, stat.Size())
            num, err := f.Read(data)
            check(err)

            type Metadata struct {
                GroupId string `xml:"groupId"`
                ArtifactId string `xml:"artifactId"`
                Versioning struct {
                    Latest string `xml:"latest"`
                    Release string `xml:"release"`
                    Versions []string `xml:"versions>version"`
                } `xml:"versioning"`
            };
            var value Metadata
            check(xml.Unmarshal(data[:num], &value))

            fun := L.CheckFunction(2)
            for _, v := range value.Versioning.Versions {
                check(L.CallByParam(lua.P{
                    Fn: fun,
                    NRet: 0,
                    Protect: true,
                }, lua.LString(v)))
            }

            return 0;
        },
        "pretty_print": func(l *lua.LState) int {
            thing := L.CheckAny(1)
            pp.Println(thing)
            return 0;
        },
        "canonical_path": func(l *lua.LState) int {
            path := L.CheckString(1)
            abs, err := filepath.Abs(path)
            if err != nil {
                L.Push(lua.LNil)
                L.Push(lua.LString(err.Error()))
                return 2;
            }
            L.Push(lua.LString(abs))
            return 1;
        },
    })
    L.SetGlobal("Jice", jice_tbl)

    for k, v := range config.Plugins {
        a := any_to_lua(L, v.Config)
        // pp.Println(a)
        L.SetGlobal("config", a)
        var plugin []byte;
        if strings.HasPrefix(v.Url, "file://") {
            thing, _ := strings.CutPrefix(v.Url, "file://");
            contents, err := os.ReadFile(thing)
            check(err);
            plugin = contents
        } else {
            contents, err := get_or_cache(v.Url, k + ".lua", "plugins")
            plugin = contents
            check(err)
        }
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

func apply_config(config JiceConfig, second JiceConfig) JiceConfig {
    config.Package.SourceDir = append(config.Package.SourceDir, second.Package.SourceDir...)
    config.Package.JavacArgs = append_optional_array_to_optional_array(config.Package.JavacArgs, second.Package.JavacArgs)
    config.Package.Resources = append_optional_array_to_optional_array(config.Package.Resources, second.Package.Resources)
    config.Package.DefaultRepo = second.Package.DefaultRepo
    if second.Package.Main != nil {
        config.Package.Main = second.Package.Main
    }
    config.Package.Classpath = append_optional_array_to_optional_array(config.Package.Classpath, second.Package.Classpath)
    
    if config.Dependencies == nil { config.Dependencies = make(map[string]string) }
    for k, v := range second.Dependencies {
        config.Dependencies[k] = v
    }

    if config.Repos == nil { config.Repos = make(map[string]string) }
    for k, v := range second.Repos {
        config.Repos[k] = v
    }

    if config.Extra == nil { config.Extra = make(map[string]string) }
    for k, v := range second.Extra {
        config.Extra[k] = v
    }

    for k, plugin := range second.Plugins {
        config_plugin, ok := config.Plugins[k]
        if !ok {
            config.Plugins[k] = plugin
        } else {
            if plugin.Url != "" {
                config_plugin.Url = plugin.Url
            }
            for ck, cv := range plugin.Config {
                config_plugin.Config[ck] = cv
            }
            config.Plugins[k] = config_plugin
        }
    }
    return config
}

func append_optional_array_to_optional_array[T any](a *[]T, b *[]T) *[]T {
    if a == nil { return b }
    if b == nil { return a }
    ab := append(*a, *b...)
    return &ab
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

    args := os.Args[1:];
    if len(args) == 0 {
        fmt.Println("no args");
        return;
    }
    variants := []string{}
    i := 0
    for true {
        if i >= len(args) { break }
        if args[i] == "-v" || args[i] == "--variant" {
            variants = append(variants, args[i + 1])
            args = slices.Delete(args, i, i + 2)
        }
        i += 1
    }

    for _, v := range variants {
        for k, c := range config.Variants {
            if v == k {
                config = apply_config(config, c)
            }
        }
    }

    config.L = L;
    if config.Package.DefaultRepo == "" {
        config.Package.DefaultRepo = "https://repo1.maven.org/maven2";
    }

    if args[0] == "build" {
        deps := get_all_deps_from_config(config);
        init_lua(config, deps)
        ga2dep := make(GroupArtifactToDep);
        make_ga2dep(ga2dep, deps);
        build(config, ga2dep);
        // dep := get_all(jice, "https://repo1.maven.org/maven2", "com.google.guava", "guava", "33.3.1-jre")
        // project := get_dep(jice, "https://repo1.maven.org/maven2", "com.google.errorprone", "error_prone_annotations", "2.28.0")
    } else if args[0] == "clean" {
        os.RemoveAll("./.jice")
    } else if args[0] == "tree" {
        tree(get_all_deps_from_config(config), 0, "")
    } else if args[0] == "doc" {
        javas, err := get_source_files(config);
        check(err);

        args := []string{
            "-d", "./.jice/doc/",
            "-cp", "./.jice/cache/*",
            "-private",
        }
        args = append(args, javas...);

        cmd := exec.Command("javadoc", args...);
        cmd.Stdout = os.Stdout;
        cmd.Stderr = os.Stderr;

        err = cmd.Run();
        check(err);

        exec.Command("xdg-open", "./.jice/doc/index.html").Start();
    } else if args[0] == "info" {
        pkg := args[1]
        stuff := strings.Split(pkg, ":")

        deps := get_all_deps_from_config(config)
        ga2dep := make(GroupArtifactToDep);
        make_ga2dep(ga2dep, deps)
        
        var dep *Dependency;
        for k, d := range ga2dep {
            if k.GroupId == stuff[0] && k.ArtifactId == stuff[1] {
                dep = &d
                break
            }
        }
        if dep != nil {
            if dep.Description != nil {
                fmt.Println("Description:", *dep.Description)
            }
            fmt.Println("Repo:", dep.Repo)
            fmt.Println("Version:", dep.Version)
            if dep.Url != nil {
                fmt.Println("Url:", *dep.Url)
            }
            fmt.Printf("%d dependencies\n", len(dep.Dependencies))
        } else {
            fmt.Println("Could not find package")
        }
    } else {
        fmt.Println("Unknown arg: " + args[0])
    }
    pp.Print()
}