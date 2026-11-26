# hxdna/provision

The provisioning line of business's worker contracts. Its own Go module inside the hxdna repo
(tags `provision/v0.x.y`), so the common hxdna core never learns provisioning words.

```
go get github.com/ivedhahpc/hxdna/provision@v0.2.0
```

Design: `docs/architecture/provision-contracts.md` in the hive docs repo.

## What's here

- **BuildContract** (virtual machines): the commands every VM worker registers
  (`RequiredCommands`), optional abilities (`resize`, `reimage`, `snapshot`, `console`), and the
  `platform_options` keys a worker accepts. Stored in `hxdna.Manifest.Extensions["provision.build"]`
  via `SetBuildContract` / `BuildContractOf`.
- **BuildRequest**: the build's values as params (`Params`, control-plane side) and parsed and
  checked (`ParseBuildRequest`, worker side). Bad values come back `hxdna.Terminal`.
- **Answers**: `StepResult`, `TaskStatus`, `InstanceStatus`, and `VMDetails` for the common
  Inventory contract (`hxdna.InventoryItem.Details`).
- **Build label**: `BuildLabel(id)` is written onto every machine a build makes; `BuildIDFrom`
  reads it back. Workers act only on machines carrying the asking build's label.

## In a worker

```go
m := hxdna.Manifest{WorkerType: "proxmox-worker", Commands: cmds}
_ = provision.SetBuildContract(&m, provision.BuildContract{
    Optional:        []string{provision.AbilityConsole},
    PlatformOptions: []string{"storage"},
})

// in a test: the manifest must register every command it promises
if missing := provision.MissingCommands(m); len(missing) > 0 { t.Fatal(missing) }

// in a handler
req, err := provision.ParseBuildRequest(cmd.Params, []string{"storage"})
if err != nil { return nil, err } // already hxdna.Terminal
```

## Not here yet

**InstallContract** (bare metal) is a draft until it can be tested (a Proxmox VM with a virtual BMC).
