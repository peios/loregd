"""Small host checks with explicitly declared runfiles."""

def _host_test_impl(ctx):
    launcher = ctx.actions.declare_file(ctx.label.name + ".sh")
    ctx.actions.write(launcher, "#!/bin/bash\nset -euo pipefail\ncd \"${RUNFILES_DIR:-$0.runfiles}/%s\"\n%s\n" % (ctx.workspace_name, ctx.expand_location(ctx.attr.command, targets = ctx.attr.data)), is_executable = True)
    return [DefaultInfo(executable = launcher, runfiles = ctx.runfiles(files = ctx.files.data))]

host_test = rule(implementation = _host_test_impl, test = True, attrs = {"command": attr.string(mandatory = True), "data": attr.label_list(allow_files = True)}, doc = "Run a host check against its declared inputs.")
