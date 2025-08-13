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
        local should_map = false
        for r, _ in pairs(config.mapped) do -- TODO: Stupid kdl
            local a = r;
            local b = d.repo;
            if string.sub(a, string.len(a), string.len(a)) == "/" then
                a = string.sub(a, 1, string.len(a) - 1);
            end
            if string.sub(b, string.len(b), string.len(b)) == "/" then
                b = string.sub(b, 1, string.len(b) - 1);
            end
            if a == b or b == "from extra deps" then
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
                    assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/cache/" .. path .. " ./.jice/cache/" .. path .. " ./.jice/mapping/off2inter.tiny official intermediary >/dev/null"))
                    assert(os.execute("java -jar ./.jice/mapping/remapper.jar ./.jice/cache/" .. path .. " ./.jice/cache/" .. path .. " ./.jice/mapping/inter2named.tiny intermediary named >/dev/null"))
                end
            end
            table.insert(already_did, path)
        end
        count = count + 1;
        do_map(d.dependencies, already_did, max_deps)
    end
end

local function count_deps(deps)
    local asd = #deps
    for _, d in pairs(deps) do
        asd = asd + count_deps(d.dependencies)
    end
    return asd
end

function Plugin.before_build()
    if config.map == "" or config.map == nil then
        error("Missing map url")
    end
    if config.intermediary == "" or config.intermediary == nil then
        error("Missing intermediary url")
    end
    assert(Jice.get_or_cache(config.map, "mappings.jar", "mapping"))
    assert(Jice.get_or_cache(config.intermediary, "intermediary.jar", "mapping"))
    assert(os.execute("cd ./.jice/mapping && unzip -qjo mappings.jar && mv mappings.tiny inter2named.tiny"));
    assert(os.execute("cd ./.jice/mapping && unzip -qjo intermediary.jar && mv mappings.tiny off2inter.tiny"));
    assert(Jice.get_or_cache(
        "https://maven.fabricmc.net/net/fabricmc/tiny-remapper/0.9.0/tiny-remapper-0.9.0-fat.jar",
        "remapper.jar",
        "mapping"
    ))
    local deps = Jice.get_dependencies()
    count = 0
    do_map(deps, {}, count_deps(deps))
end
return Plugin