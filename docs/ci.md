# Checks

Dagger Cloud runs the checks of this repository for each push and for each
pull request. The configuration is in `dagger.toml`.

| Check | What it does |
| --- | --- |
| `load` | Loads the modules of the workspace and lists the checks. |
| `go/modules/packages/tests/test` | Runs the Go tests of each package. |
| `go/modules/packages/generate/stale` | Fails when the generated Go files are not up to date. |
| `dagger-go-sdk/generate/stale` | Fails when the generated code of the Dagger module in `.dagger/modules/test-env` is not up to date. |

Each check reports its result to the commit on GitHub, with a link to its
trace in Dagger Cloud.
