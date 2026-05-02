local Plugin = {}
local function file_exists(path)
    local f = io.open(path, "r")
    if f ~= nil then
        io.close(f)
        return true
    else
        return false
    end
end

local count = 0;
local function do_map(deps, already_did, max_deps)
    local extra = ""
    if count > 0 then
        extra = "\27[1A"
    end
    for _, d in pairs(deps) do
        local should_map = d.repo == "mc plugin minecraft"
        for r, _ in pairs(config.mapped) do -- TODO: Stupid kdl
            local a = r;
            local b = d.repo;
            if string.sub(a, string.len(a), string.len(a)) == "/" then
                a = string.sub(a, 1, string.len(a) - 1);
            end
            if string.sub(b, string.len(b), string.len(b)) == "/" then
                b = string.sub(b, 1, string.len(b) - 1);
            end
            if a == b then
                should_map = true
                break
            end
        end
        if should_map then
            local path = string.format(
                "%s-%s.jar",
                Jice.query_escape(d.group),
                Jice.query_escape(d.artifact)
            );
            local should_really_map = true
            if already_did[path] then
                should_really_map = false
            end
            if should_really_map then
                print(extra .. "\27[9999DMapping " .. tostring(count) .. "/" .. tostring(max_deps) .. "...") -- TODO: This doesnt actually show max deps since it counts same ones multible times
                if file_exists("./.jice/cache/" .. path) then
                    assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/cache/" .. path .. " /tmp/jice_mc_mapped.jar ./.jice/mapping/mappings.tiny official named >/dev/null"))
                    assert(os.execute("mv /tmp/jice_mc_mapped.jar ./.jice/cache/" .. path))
                    -- assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/cache/" .. path .. " ./.jice/cache/" .. path .. " ./.jice/mapping/inter2named.tiny intermediary named >/dev/null"))
                end
                already_did[path] = 1
            end
        end
        count = count + 1;
        do_map(d.dependencies, already_did, max_deps)
    end

    return already_did
end

local function count_deps(deps)
    local asd = #deps
    for _, d in pairs(deps) do
        asd = asd + count_deps(d.dependencies)
    end
    return asd
end

local function java_type_to_jvm(type)
    if type == "boolean" then
        return "Z"
    elseif type == "byte" then
        return "B"
    elseif type == "char" then
        return "C"
    elseif type == "short" then
        return "S"
    elseif type == "int" then
        return "I"
    elseif type == "long" then
        return "J"
    elseif type == "float" then
        return "F"
    elseif type == "double" then
        return "D"
    else
        return "L" .. type:gsub("%.", "/") .. ";"
    end
end

local function make_jvm_function(type, args)
    local desc = "("
    for i = 1, #args do
        desc = desc .. java_type_to_jvm(args[i])
    end
    return desc .. ")" .. java_type_to_jvm(type)
end

local function parse_java_type(str)
    local type, name, args = str:match("^(.-) (.-)%((.-)%)$")
    if type ~= nil then
        local aargs = {}
        for part in string.gmatch(args, "([^,]+)") do
            table.insert(aargs, part)
        end
        return {
            name = name,
            type = type,
            args = aargs
        }
    end

    local type, name = str:match("^(.-) (.-)$")
    if type ~= nil then
        return {
            name = name,
            type = type
        }
    end

    error("invalid")
end

local function convert_mojang_mappings_to_tiny(path, already_did)
    local f = io.open(path, "r")
    assert(f ~= nil)
    local stack = {}
    while true do
        ---@type string
        local line = f:read("*l")
        if line == nil or line == "" then
            break
        end
        if line:sub(1, 1) == "#" then
            goto continue
        end

        if line:sub(1, 1) == " " then
            -- TODO: This is stupid
            while line:sub(1, 1) == " " do
                line = line:sub(2, -1)
            end
            line = line:gsub("^%d+:%d+:", "")

            local type, obfuscated = line:match("^(.-)%s%->%s(.-)$")
            if obfuscated == "<init>" or obfuscated == "<clinit>" then
                goto continue
            end
            table.insert(stack[#stack].inner, {
                type = type,
                obfuscated = obfuscated
            })
        else
            local class, obfuscated = line:match("^(.-)%s%->%s(.-):$")
            table.insert(stack, {
                class = class,
                obfuscated = obfuscated,
                inner = {}
            })
        end
        ::continue::
    end
    f:close()

    local tiny = ""
    for _, class in pairs(stack) do
        if already_did[class.class] ~= nil then
            goto continue
        end
        already_did[class.class] = 1

        local c = class.class:gsub("%.", "/")

        local line = "CLASS\t" .. c .. "\t" .. class.obfuscated .. "\n"
        for _, inner in pairs(class.inner) do
            local p = parse_java_type(inner.type)
            if p.args then
                local jvm = make_jvm_function(p.type, p.args)
                line = line .. "METHOD\t" .. c .. "\t" .. jvm .. "\t" .. p.name .. "\t" .. inner.obfuscated .. "\n"
            else
                local jvm = java_type_to_jvm(p.type)
                line = line .. "FIELD\t" .. c .. "\t" .. jvm .. "\t" .. p.name .. "\t" .. inner.obfuscated .. "\n"
            end
        end
        tiny = tiny .. line
        ::continue::
    end
    return tiny
end

function Plugin.before_build()
    if config.version == "" or config.version == nil then
        error("Missing `version` field in config")
    end
    if config.modid == "" or config.modid == nil then
        error("Missing `modid` field in config")
    end
    if config.map_type == "" or config.map_type == nil then
        error("Missing `map_type` field in config")
    end
    if config.map_type ~= "yarn" and config.map_type ~= "official" and config.map_type ~= "none" then
        error("Invalid `map_type` field in config. Expected yarn, official, or none")
    end
    assert(Jice.get_or_cache("https://piston-meta.mojang.com/mc/game/version_manifest_v2.json", "versions.json", "mapping"))

    local manifest = assert(Jice.read_json("./.jice/mapping/versions.json"))
    local found = nil
    for i = 0, #manifest.versions do
        local v = manifest.versions[i]
        if v == nil then break end

        if v.id == config.version then
            found = v
            break
        end
    end
    if found == nil then
        error("Couldnt find version `" .. config.version .. "`")
    end

    assert(Jice.get_or_cache(found.url, "version.json", "mapping"))
    local version = assert(Jice.read_json("./.jice/mapping/version.json"))
    assert(Jice.get_or_cache(version.downloads.client.url, "net.minecraft-client.jar", "cache"))
    assert(Jice.get_or_cache(version.downloads.server.url, "server.jar", "mapping"))

    assert(os.execute("unzip -j -u -q .jice/mapping/server.jar -d .jice/cache/ \"META-INF/libraries/*\""))
    assert(os.execute("unzip -j -q -p .jice/mapping/server.jar META-INF/versions/" .. config.version .. "/server-" .. config.version .. ".jar > .jice/cache/net.minecraft-server.jar"))

    if config.map_type == "yarn" then
        assert(Jice.get_or_cache("https://maven.fabricmc.net/net/fabricmc/yarn/maven-metadata.xml", "metadata.xml", "mapping"))
        
        local stuff = {}

        Jice.get_yarn_metadata_xml_versions("./.jice/mapping/metadata.xml", function (v)
            local split = {}
            for part in string.gmatch(v, "([^+]+)") do
                table.insert(split, part)
            end
            if split[2] == nil then return end

            local build = {}
            for part in string.gmatch(split[2], "([^%.]+)") do
                table.insert(build, part)
            end

            if build[1] ~= "build" then return end
            if build[2] == nil then return end

            local num = tonumber(build[2])
            if num == nil then return end

            if stuff[split[1]] == nil then
                stuff[split[1]] = num
            else
                if stuff[split[1]] < num then
                    stuff[split[1]] = num
                end
            end
        end)
        
        if stuff[config.version] == nil then
            error("version not in yarn metadata xml")
        end

        local thing = config.version .. "+build." .. tostring(stuff[config.version])
        local url = "https://maven.fabricmc.net/net/fabricmc/yarn/" .. thing .. "/yarn-" .. thing .. ".jar"

        assert(Jice.get_or_cache(url, "mappings.jar", "mapping"))
        assert(Jice.get_or_cache("https://maven.fabricmc.net/net/fabricmc/intermediary/" .. config.version .. "/intermediary-" .. config.version .. ".jar", "intermediary.jar", "mapping"))
        assert(os.execute("cd ./.jice/mapping && unzip -qjo mappings.jar"));
    end

    if config.map_type == "official" then
        assert(Jice.get_or_cache(version.downloads.client_mappings.url, "client.txt", "mapping"))
        assert(Jice.get_or_cache(version.downloads.server_mappings.url, "server.txt", "mapping"))
        local f = io.open("./.jice/mapping/mappings.tiny", "r")
        if f ~= nil then
            f:close()
            goto out
        end

        print("Generating mappings.tiny from official mappings")
        local already_did = {}
        local tiny = convert_mojang_mappings_to_tiny("./.jice/mapping/client.txt", already_did);
        tiny = tiny .. convert_mojang_mappings_to_tiny("./.jice/mapping/server.txt", already_did);
        tiny = "v1\tnamed\tofficial\n" .. tiny
        
        f = io.open("./.jice/mapping/mappings.tiny", "w")
        assert(f ~= nil)
        assert(f:write(tiny))
        f:close()
    end
    ::out::

    if config.map_type ~= "none" then
        assert(Jice.get_or_cache(
            "https://maven.fabricmc.net/net/fabricmc/tiny-remapper/0.9.0/tiny-remapper-0.9.0-fat.jar",
            "remapper.jar",
            "mapping"
        ))

        local f = io.open("./.jice/mapping/cache.json")
        if f ~= nil then
            f:close()
        else
            Jice.write_json("./.jice/mapping/cache.json", {})
        end
    end

    local deps = Jice.get_dependencies()
    table.insert(deps, {
        group = "net.minecraft",
        artifact = "client",
        version = config.version,
        name = "client",
        repo = "mc plugin minecraft",
        dependencies = {}
    })
    table.insert(deps, {
        group = "net.minecraft",
        artifact = "server",
        version = config.version,
        name = "client",
        repo = "mc plugin minecraft",
        dependencies = {}
    })
    if config.map_type ~= "none" then
        count = 0
        local already_did = Jice.read_json("./.jice/mapping/cache.json")
        local stuff = do_map(deps, already_did, count_deps(deps))
        Jice.write_json("./.jice/mapping/cache.json", already_did)
    end
end

function Plugin.after_build()
    local namespace
    if config.map_type == "yarn" then
        namespace = "intermediary"
    elseif config.map_type == "official" then
        namespace = "official"
    else
        error()
    end
    local temp = os.tmpname()
    local file = io.open(temp, "w")
    assert(file ~= nil)
    file:write("Fabric-Jar-Type: classes\
Fabric-Loom-Mixin-Remap-Type: mixin\
Fabric-Minecraft-Version: " .. config.version .. "\
Fabric-Mixin-Group: net.fabricmc\
Fabric-Mapping-Namespace: " .. namespace)
    file:close()
    assert(os.execute("jar ufm ./.jice/build.jar " .. temp))
    assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/build.jar ./.jice/build.jar ./.jice/mapping/mappings.tiny named " .. namespace .. " >/dev/null"))
end

function Plugin.javac_args()
    local namespace
    if config.map_type == "yarn" then
        namespace = "intermediary"
    elseif config.map_type == "official" then
        namespace = "official"
    else
        error()
    end

    local path = assert(Jice.canonical_path("./.jice/output/" .. config.modid .. ".refmap.json"))
    return {
        "-processor", "org.spongepowered.tools.obfuscation.MixinObfuscationProcessorTargets,org.spongepowered.tools.obfuscation.MixinObfuscationProcessorInjection",
        "-AinMapFileNamedIntermediary=./.jice/mapping/mappings.tiny",
        "-AoutRefMapFile=" .. path,
        "-AdefaultObfuscationEnv=named:" .. namespace
    }
end
return Plugin