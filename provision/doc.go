// Package provision holds the provisioning line of business's worker contracts — its own Go
// module inside the hxdna repo, versioned on its own (tags provision/v0.x.y), so the common
// hxdna core never learns provisioning words and a change here never forces every worker to
// release.
//
// BuildContract (virtual machines) is the contract every VM worker (Proxmox, ESXi, CloudStack…)
// implements, so Provision can build on any of them the same way and swap one for another
// without touching its catalog. The bare-metal InstallContract is a draft (not here yet) until
// it can be tested.
//
// Design: docs/architecture/provision-contracts.md in the hive docs repo.
package provision
