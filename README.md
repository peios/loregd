# loregd

loregd stores Peios registry hives in SQLite and serves them to the kernel
through the Registry Source Interface (RSI). The kernel owns namespaces,
access checks, layers, and watches; loregd owns persistent storage and the
separate tmpfs databases used for volatile keys.

The daemon runs as SYSTEM on a Peios kernel with RSI lifetime extension
version 1. A Linux development host can build it and run the unit tests;
those tests do not establish native registry or boot qualification.

## Build and test

Build the daemon, run the tests, and produce the package store:

```sh
./bazel build //:loregd
./bazel test //...
./bazel build //:packages
```

The package store is `bazel-bin/packages.store`. See
[Building and Testing](docs/building.md) for prerequisites and validation
boundaries.

## Documentation

The documentation entry points are:

- [Runtime and storage manual](docs/learn/peios.product/3--advanced-peios.antho/300--trms.shelf/300--loregd.book/1--introduction/1--overview.md)
- [Operating loregd](docs/learn/peios.product/750--registry-storage.shelf/100--local-registry.topic/100--operating-loregd.md)
- [Installed manual page](docs/man/loregd.8)
- [Documentation maintenance](docs/documentation.md)
- [Security reporting](SECURITY.md)

`docs/learn/` merges into the Peios product in Panorama. Historical source
documents and their hashes are in `docs/archive/`; they are not published
as maintained Learn content.

## License

The daemon uses the license in [LICENSE](LICENSE). Package metadata records
the notices for the selected Go dependency sources.
