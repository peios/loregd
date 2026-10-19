"""Stage a declared Go binary, source closure and dependency notices."""

load("//build:version.bzl", "VERSION")

def _stage_impl(ctx):
    output = ctx.actions.declare_directory(ctx.label.name)
    manifest = ctx.actions.declare_file(ctx.label.name + ".inputs.json")
    sdk = ctx.toolchains["@rules_go//go:toolchain"].sdk
    inputs = ctx.files.data + ctx.files.drivers + ctx.files.go_sources + [ctx.file.binary, sdk.go]
    inputs += sdk.srcs.to_list() + sdk.tools.to_list()
    ctx.actions.write(manifest, json.encode({
        "binary": ctx.file.binary.path,
        "go_sources": ctx.file.go_sources.path,
        "go": sdk.go.path,
        "go_version": sdk.version,
        "sdk_root": sdk.root_file.dirname,
        "sdk_sources": [f.path for f in sdk.srcs.to_list()],
        "version": VERSION,
        "native": ctx.attr.native,
        "sources": [{"path": f.path, "relative": f.short_path} for f in ctx.files.data],
    }))
    ctx.actions.run(
        executable = "/usr/bin/python3",
        arguments = [ctx.file.driver.path, manifest.path, output.path],
        inputs = depset(inputs + [manifest]),
        outputs = [output],
        env = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"},
        execution_requirements = {"no-sandbox": "1", "no-remote-exec": "1"},
        mnemonic = "LoregdPackageStage",
    )
    return [DefaultInfo(files = depset([output]))]

daemon_stage = rule(
    implementation = _stage_impl,
    toolchains = ["@rules_go//go:toolchain"],
    attrs = {
        "binary": attr.label(allow_single_file = True, mandatory = True),
        "go_sources": attr.label(allow_single_file = True, mandatory = True),
        "data": attr.label_list(allow_files = True),
        "drivers": attr.label_list(allow_files = True),
        "driver": attr.label(allow_single_file = True),
        "native": attr.bool(),
    },
    doc = "Stage the selected pure-Go binary with split debug files and source provenance.",
)
