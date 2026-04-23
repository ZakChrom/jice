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
    print(extra .. "\27[9999DMapping " .. tostring(count) .. "/" .. tostring(max_deps) .. "...") -- TODO: This doesnt actually show max deps since it counts same ones multible times
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
                "%s-%s-%s.jar",
                Jice.query_escape(d.group),
                Jice.query_escape(d.artifact),
                Jice.query_escape(d.version)
            );
            local should_really_map = true
            for _, v in pairs(already_did) do
                if v == path then
                    should_really_map = false
                    break
                end
            end
            if should_really_map then
                if file_exists("./.jice/cache/" .. path) then
                    assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/cache/" .. path .. " ./.jice/cache/" .. path .. " ./.jice/mapping/mappings.tiny official named >/dev/null"))
                    -- assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/cache/" .. path .. " ./.jice/cache/" .. path .. " ./.jice/mapping/inter2named.tiny intermediary named >/dev/null"))
                end
                table.insert(already_did, path)
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

function Plugin.before_build()
    if config.version == "" or config.version == nil then
        error("Missing `version` field in config")
    end
    if config.modid == "" or config.modid == nil then
        error("Missing `modid` field in config")
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
    assert(Jice.get_or_cache(version.downloads.client.url, "net.minecraft-client-" .. config.version .. ".jar", "cache"))

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
    -- assert(os.execute("cd ./.jice/mapping && unzip -qjo intermediary.jar && mv mappings.tiny off2inter.tiny"));
    assert(Jice.get_or_cache(
        "https://maven.fabricmc.net/net/fabricmc/tiny-remapper/0.9.0/tiny-remapper-0.9.0-fat.jar",
        "remapper.jar",
        "mapping"
    ))

    local f = io.open("./.jice/mapping/cache.kdl")
    if f ~= nil then
        f:close()
    else
        Jice.write_json("./.jice/mapping/cache.json", {
            cache = {}
        })
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
    count = 0
    local stuff = do_map(deps, Jice.read_json("./.jice/mapping/cache.json").cache, count_deps(deps))

    Jice.write_json("./.jice/mapping/cache.json", {
        cache = stuff
    })
end

function Plugin.after_build()
    local temp = os.tmpname()
    local file = io.open(temp, "w")
    assert(file ~= nil)
    file:write("Fabric-Jar-Type: classes\
Fabric-Loom-Mixin-Remap-Type: mixin\
Fabric-Minecraft-Version: " .. config.version .. "\
Fabric-Mixin-Group: net.fabricmc\
Fabric-Mapping-Namespace: intermediary")
    file:close()
    assert(os.execute("jar ufm ./.jice/build.jar " .. temp))
    assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/build.jar ./.jice/build.jar ./.jice/mapping/mappings.tiny named intermediary >/dev/null"))
end

function Plugin.javac_args()
    local path = assert(Jice.canonical_path("./.jice/output/" .. config.modid .. ".refmap.json"))
    return {
        "-processor", "org.spongepowered.tools.obfuscation.MixinObfuscationProcessorTargets,org.spongepowered.tools.obfuscation.MixinObfuscationProcessorInjection",
        "-AinMapFileNamedIntermediary=./.jice/mapping/mappings.tiny",
        "-AoutRefMapFile=" .. path,
        "-AdefaultObfuscationEnv=named:intermediary"
    }
end
return Plugin