# Develop inside a Firecracker microVM

`scripts/firecracker-setup.sh` prepares an optional persistent Linux development machine
for Codex. Docker Engine, application containers, volumes and builds live inside
the guest. The host's Docker socket and filesystem are not mounted into it.
Almanac still builds and tests through its existing Docker workflow
([ADR-0029](adr/0029-platform-agnostic-development-and-verification.md)).
Prepared bases and worker isolation follow
[ADR-0045](adr/0045-prepared-firecracker-development-workers.md).

The launcher supports independent named x86_64 Linux guests on one host. It uses
Firecracker's jailer, an Ubuntu 24.04 filesystem and a Docker-capable Linux 6.18
guest kernel. Firecracker, the source filesystem, kernel and Codex downloads are
pinned and checked with SHA-256. Guest apt packages receive current updates from
Ubuntu and Docker's signed repositories. This is a development setup, not a
production sandbox service.

## Host requirements

- Hardware virtualization, working `/dev/kvm` access and cgroup v2.
- For automatic login transfer, a file-based Codex host login and the host GitHub
  CLI (`gh`) already authenticated to `github.com`.
- Stencil Tern installed on the laptop's PATH. `vm-base` copies its executable
  into the guest image; `vm-run` uses the local app to connect to workers.
- `bash`, `curl`, `python3`, Git, OpenSSH client, `tar`, coreutils, `flock`,
  `unsquashfs`, `mkfs.ext4`, `mount`, `umount`, `find`, `ip`, `iptables`,
  `ip6tables` and `sudo`.
- A reflink-capable filesystem, such as Btrfs or reflink-enabled XFS, for the
  shared state directory. Bases and workers must be on the same filesystem.
  `cp --reflink=always` fails instead of silently making a full disk copy.
- IPv4 forwarding already enabled. The launcher does not change host sysctls or
  firewall policies; it installs and removes rules for its own TAP interface.
- Sudo access for creating the filesystem, TAP interface and jail. Run the script
  as your normal user; it prompts through sudo when needed. Reading an older
  guest's root-owned PID file can also require sudo, including for `status` or
  `list`; new starts give the launcher ownership of that file. A PID-file
  read or process-inspection failure stops the command while preserving the VM;
  it is never treated as proof that the VM has stopped.
- Resources for each named guest's default 2 vCPU / 4 GiB RAM / 80 GiB sparse disk.
  The original unnamed guest retains 4 vCPU / 8 GiB RAM. Budget total RAM across
  running guests; full browser/integration work may need a larger guest.
  Disks grow as images, build caches and application data accumulate.

For example, Arch's relevant packages are `squashfs-tools`, `e2fsprogs`,
`iproute2`, `iptables-nft`, `findutils`, `openssh`, `python`, `git`, `curl`, `util-linux`
and `sudo`. Install missing host packages yourself; the launcher never runs a
host package manager. Host Docker is not required.

Each named guest reserves a stable, distinct /30 subnet in `192.168.240.0/20`,
TAP, MAC address and firewall chain. Allocation is serialized and supports up to
1,022 names per state directory, excluding the legacy guest's subnet. A reservation stays with a stopped guest so its
disk can boot with the same network. All linked worktrees automatically share
the primary checkout's `.local/firecracker/`, so `make vm-run` needs no state
override and `make vm-list` sees the same workers from every checkout.
The original unnamed guest keeps `almfc0` on
`192.168.241.0/30`; its existing disk and SSH configuration are preserved.
Setup refuses an existing TAP or overlapping route rather than changing it.
Host IPv4 input rules reject new connections from the guest. Forwarding rules
block common private and special IPv4 ranges, including RFC 1918, carrier-grade
NAT, loopback and link-local destinations. Other host-routed LAN or VPN ranges,
such as `198.18.0.0/15` or publicly addressed internal networks, remain reachable
unless the host firewall blocks them separately. Host-initiated SSH and its
replies are allowed. A corporate proxy, private registry or VPN-only datasource
may need a separately reviewed network change.
IPv6 is disabled in the guest kernel at boot. Host `ip6tables` rules also drop
IPv6 input and forwarding from the TAP before it is activated, covering raw
frames sent by guest administrators. Existing running guests receive these
host rules on their next stop/start cycle with the updated launcher.
Guests cannot initiate connections to each other through their private networks.

## First use

From the repository root:

```sh
bash scripts/firecracker-setup.sh check
gh auth login --hostname github.com # if the host is not already logged in
make vm-base
make vm-run VM=api VM_PROMPT='Investigate the API issue'
make vm-codex VM=api
```

`vm-base` creates a fresh seed, installs Docker Engine, Compose, buildx, GitHub
CLI, tmux, the matching Stencil Tern runtime/session helper and the complete
pinned Codex runtime, including its command-execution helpers and resources.
It runs `make setup`, stops the fixtures and shuts down
the seed before copying its disk. The base retains development images, Playwright,
Go/npm dependencies and Docker build caches. No worker keys, logins or Tern
session state enter the base; those are configured after cloning. Stopped seeds are preserved.
Private local generations live in `.local/firecracker/base-images/`, with an
atomic `base-image` pointer to the current complete generation.
An interrupted rebuild can leave an unpublished raw seed copy under
`base-images/image.*`, including seed credentials if sanitization failed. These
private copies remain available for inspection and manual cleanup. Publication
updates the base pointer only after sanitization succeeds.

`setup` and `run` require a prepared base. They create writable CoW clones with
fresh SSH keys, hostname, machine identity and networking, then copy the current
checkout's Git history, tracked files and non-ignored untracked files,
including uncommitted changes. Ignored local data and the host Git database are
not shared. The guest's independent checkout is `/home/dev/almanac`; Codex starts
in its dedicated `/home/dev/almanac/.worktrees/codex` worktree on `codex/NAME`.
If that branch already exists in the source, a numeric suffix preserves it.
The host's Git author name/email and a sanitized GitHub HTTPS `origin` are copied.
SSH agents and global Codex plugins/configuration are not forwarded.

For normal Codex tasks, use `vm-base` once and `vm-run` for each worker. `vm-run`
performs worker setup automatically. The separate `vm-setup` command prepares a
worker for manual shell work and does not launch Codex.

By default, the copied source is the checkout containing the launcher. Use
`--repo /path/to/checkout` on first `setup` or `run` to select another checkout
or linked worktree; `VM_REPO=/path/to/checkout` selects it through Make. The guest
receives an independent snapshot, not a shared
host mount. Later host changes are not synchronized over guest edits.

If file-based Codex authentication is available, setup copies `auth.json` once
through SSH into the guest's private home. It does not print the file or overwrite
credentials on subsequent runs. Otherwise open a guest shell and run
`codex login --device-auth`. Tokens are account credentials, so keep the entire
state directory private. Concurrent copies of a login can require a new login
after token rotation; an independent guest login avoids sharing that cache.
See [Codex authentication](https://developers.openai.com/codex/auth/).

When host `gh` authentication is available, setup sends its `github.com` token
through encrypted SSH stdin to `gh auth login --with-token` in the worker.
`gh auth setup-git` configures HTTPS pushes and PRs. No host SSH private key is
copied. Tokens stay in the worker's private home and inherit the host token's
permissions; they are added after cloning, outside the base. Without a host token,
setup prints the guest login command. See
[GitHub CLI login](https://cli.github.com/manual/gh_auth_login) and
[Git credential setup](https://cli.github.com/manual/gh_auth_setup-git).

Development is ready without another `make setup`:

```sh
make vm-ssh VM=api
# Inside the guest:
make test
```

`make check`, `make test-e2e` and other commands in [the catalog](commands.md)
work the same way. The Docker socket mounted into the E2E development container
belongs to the guest. Both `dev` and Codex can administer that guest, including
running containers; they receive no host Docker access.

## Rebuild or adopt an existing seed

Run `make vm-base` again to rebuild all dependencies from the current definition
and checkout. Each invocation creates a fresh seed name, preserving
existing workers. Publishing is atomic: a failed build leaves the previous base
active, and existing workers keep their original disks.

To reuse an existing guest whose dependencies are already warm:

```sh
# Quit Codex and any tmux tasks in api first. This command will stop api.
make vm-base VM=api VM_REPO=/path/to/the/source/checkout
```

The command refuses active tmux sessions, non-development Docker containers or
volumes, and a seed whose dependency inputs differ from the requested checkout.
It runs `make setup` once for that generation, reusing the seed's caches. Only the
copied disk is sanitized: seed homes, checkout, credentials, logs, SSH host keys
and machine identity are removed; Docker images and dependency volumes remain.
Keep bases private: deleted files in raw disks are not forensic credential
erasure, and build caches can contain source data.

New workers match a fingerprint of the development Dockerfile, Compose file,
`make setup` recipe and Go/npm manifests/lockfiles. Ordinary source edits do not
invalidate it. Rebuild the base after dependency changes. Old generations and
stopped seeds remain available for explicit cleanup. Deleting an unused old base
generation does not invalidate its independent reflink clones.

## Daily use and persistence

```sh
make vm-list
make vm-start VM=api
make vm-ssh VM=api
make vm-codex VM=api
make vm-stop VM=api
make vm-delete VM=api # permanently remove disk, credentials and worker-local results
```

`vm-list` shows running, stopped and partial guests. Stopping preserves the disk.
Deleting requests orderly shutdown first, requires an explicit `VM=NAME`, and
releases that guest's network slot. A failed shutdown or unknown process state
preserves the guest. Unmount any filesystem left by a failed offline edit before
deleting. Deleting the legacy `almanac` guest preserves shared bases, downloads,
named guests and previously collected result directories.

## Multiple background agents and prompts

Omit `VM` to generate a two-word name, such as `fuzzy-tiger` or `sad-koala`:

```sh
cat task.txt | make vm-run
# VM: fuzzy-tiger
make vm-codex VM=fuzzy-tiger
```

Unnamed `vm-setup` and `vm-base` also generate names. Names are reserved atomically,
so concurrent launches retry collisions without sharing or overwriting a worker.
The chosen name is printed before setup, along with attachment instructions when
Codex starts. Existing lifecycle commands without `VM` still select the original
`almanac` guest; deletion always needs an explicit name. The script supports the
same behavior when `NAME` is omitted.

You can also choose a short name per task. `run` clones a guest on first use and then starts
interactive Codex in the guest's Tern session and connects your laptop's Tern window:

```sh
bash scripts/firecracker-setup.sh run api --prompt 'Investigate the API issue described in docs/task.md'
cat task.txt | bash scripts/firecracker-setup.sh run ui --prompt -
bash scripts/firecracker-setup.sh codex api
bash scripts/firecracker-setup.sh ssh ui
bash scripts/firecracker-setup.sh status api
bash scripts/firecracker-setup.sh stop ui
```

`--prompt -` reads all of stdin before setup begins, preserving quotes and
newlines. Bare `make vm-run` reads the task interactively with Backspace and
cursor editing: type it, press Enter, then Ctrl-D to launch; Ctrl-C cancels.
The launcher displays these instructions
before waiting and creates no VM until a nonempty task is supplied. To pass the
task directly, use `make vm-run VM_PROMPT='Your task here'`.
Inline script prompts are a single shell argument; quote them normally.
Prompt text is sent over SSH into a private file and passed as a Codex argument,
never evaluated as a shell program.
Use `--repo /path/to/checkout` with `run` to copy a different source on first use.

Every guest has its own disk, credentials, Docker daemon, containers, ports and
Git database. Its task worktree is `/home/dev/almanac/.worktrees/codex` on
`codex/NAME`, giving workers distinct branches for GitHub PRs. Earlier guests keep
their existing branches. A completed setup never overwrites that guest checkout. Stop one
VM without affecting the others. Separate invocations can prepare different
guests concurrently; verified downloads are cached once under the shared
`downloads/` directory and protected by locks.

Codex launched by this script uses `--sandbox danger-full-access` and
`--ask-for-approval never` **inside its guest**, allowing unattended Docker and
development commands. Repository instructions still govern commits and publishing.
The VM boundary, jailer and network rules isolate it from the host; this is not
a grant of access to host files or the host Docker daemon. See the
[Codex permission guidance](https://developers.openai.com/codex/noninteractive/).
An existing Codex session is attached unchanged; supplying a new prompt while
it exists is rejected. Quit that session before starting a new task in the same VM.
`make vm-ssh` opens the task worktree. The launcher saves trust for its common
Git root, `/home/dev/almanac`, in the guest's Codex config so typing `codex` there
uses the same trust as a background worker. Opening a shell also applies this
setting to older guests with the original launcher config.
An existing customized or symlinked `config.toml` is preserved, so manual Codex
may still ask for trust. Accept that prompt once, or merge the following setting
into `/home/dev/.codex/config.toml` inside the guest. If the project table already
exists, update its `trust_level` entry.

```toml
[projects."/home/dev/almanac"]
trust_level = "trusted"
```

The background launcher supplies this trust override on the command line.

Each guest inherits the base's warmed Docker images and dependency caches.
Several copied ChatGPT auth caches may require fresh logins after token rotation;
independent guest logins avoid sharing a refresh cache.

`make vm-run` requires [Stencil Tern](https://stencil.so/tern) on the laptop's
PATH and a usable SSH identity: an unlocked key in the SSH agent used by Tern,
or an unencrypted standard private-key file in `~/.ssh/`. A `.pub` file alone
cannot authenticate. Encrypted keys must first be unlocked locally, for example
with `ssh-add ~/.ssh/id_ed25519`; never send the passphrase to the guest.
Authentication preflight runs before allocating a worker. The launcher then boots
the worker, installs the matching Tern runtime when missing, starts its guest
services and launches Codex directly in a `codex` Tern session.
It retrieves the host fingerprint over authenticated SSH, configures local Tern
trust, then adds/selects that remote host in the local window. No manual host
entry or fingerprint copying is required. The local app opens if necessary.
An invocation from a local Tern pane targets that pane's window; other terminals
use the first local Tern window through an automatically closing control session.
Local Lua performs this connection without a model request or a custom plugin.

The guest listener binds its private IPv4 address on UDP 8376 with relays disabled.
Only public laptop SSH keys are authorized; private keys stay on the laptop.
Newly rebuilt bases already contain the runtime. Older prepared bases can still
receive it without another application `make setup`. Guest services start with the VM, and each clone has its own Tern
identity and session state. A saved identity mismatch stops connection setup.
If local connection fails after launch, the VM and agent remain running; use
`make vm-codex VM=NAME` to reconnect without submitting the task again.
An already running Codex session rejects a replacement prompt.

If Tern reports `Nothing to log in with`, unlock the key in your laptop terminal
with `ssh-add ~/.ssh/id_ed25519`, then run `make vm-codex VM=NAME` for the existing
worker. Both Tern and that terminal must use the same SSH agent. Codex and its
initial task are preserved; creating another worker is unnecessary.

Closing the local window leaves Codex running inside the VM. A stopped VM preserves files, containers,
images, volumes, login credentials and Codex conversation history, but stops
processes. Use `codex resume` inside the guest to reopen a previous conversation.
The initial task is marked consumed before Codex starts so service restoration
cannot submit it again after a guest restart. Tern restores login shells rather
than their former commands; `vm-codex` recreates the owned idle Codex pane and
waits for its real process. It preserves foreground/background work and extra
panes instead of replacing them. Stop and deletion retain their existing disk
semantics. Deletion forgets the saved host, disconnects its live window entry
and removes only the public pin matching that worker's recorded fingerprint.
Tern 0.9.16 retains pins after its ordinary forget command, so this pin cleanup
uses Tern's own file lock and preserves other host entries. A different cached
identity causes deletion to stop with the disk preserved.

Older tmux workers remain attachable through `make vm-codex`; their live
processes are preserved. Detach from those terminals with Ctrl-B then D.
Legacy attachment enables extended keys and maps Shift+Enter to a newline in the
`codex` tmux session. Enter still submits. Other tmux sessions receive their
original Shift+Enter key. This also repairs existing Codex sessions when attached
with the updated launcher. Detach and reattach to negotiate extended keys on an
older connection; the guest base and Codex process can keep running.
Stop requests an orderly guest reboot; on x86, `reboot=k` makes that exit
Firecracker rather than boot again. It starts `reboot.target` directly through
PID 1's private systemd socket, avoiding a missing-system-bus warning in minimal
guests without D-Bus/logind. This queues the normal shutdown transaction and
waits for Firecracker to exit. See the
[systemctl reboot documentation](https://www.freedesktop.org/software/systemd/man/255/systemctl.html#reboot).
Ordinary `poweroff` would leave the VMM
alive. The launcher never force-kills the VM. It removes
only this instance's network rules and TAP, keeping the disk.
Before a stopped guest starts again, the launcher removes its stale PID/API
files and the four device nodes that jailer recreates. Its disk, configuration,
logs and SSH keys are preserved.

Generated assets, private keys, SSH configuration, downloads and persistent disks
stay under the existing ignored `.local/firecracker/` directory. Named guest state
is in `instances/NAME/`; the original unnamed guest stays at the root. This is
the Git primary checkout's directory, shared by its linked worktrees. The source
snapshot still comes from the checkout where you run `make vm-run`; no `-C` or
environment override is needed. Commit
only the script, its test and documentation. Repeating setup preserves a completed
guest; it does not sync host changes over guest work. A failed image build is
preserved under the instance's `build/` directory for inspection before a retry.
Deletion removes private contents before retiring the instance directory. A
failed removal remains listed as `partial` under its original name, retaining its
network reservation so `make vm-delete VM=NAME` can finish the cleanup. Older
launchers can leave `deleted-NAME.*` directories at the state root after an
interrupted deletion. Those directories require manual inspection and removal.

Before first setup, `VM_CPUS` and `VM_MEMORY_MIB` can override the defaults.
Clones inherit the base disk size. `VM_DISK_GIB` selects the first seed's disk size;
an explicit value for clones must match the base. `VM_DISK_GIB=120 make vm-base`
builds a new 120 GiB base. Changing variables does not resize an existing guest.
The optional `FIRECRACKER_STATE_DIR` override selects another canonical absolute
directory. It is only needed for a deliberately separate pool; use the same
override for its later commands. Keep that directory outside tracked source and private.

## Preview and retrieve work

The launcher writes a private SSH configuration for each instance. For example,
forward the guest's normal Almanac preview to an unused host port:

```sh
ssh -F .local/firecracker/instances/api/ssh_config -N -L 28088:127.0.0.1:18088 dev
```

After running `make run` inside the guest, the application is reachable through
that tunnel at `https://localhost:28088`. Retrieve the initial password inside
the guest with `make credentials`.

To retrieve a named agent's work, quit Codex in its attached terminal first,
then collect. Collection refuses an existing Codex tmux session so it does not
copy files while that agent may still be changing them. A stopped guest is booted
for collection, without starting another Codex session:

```sh
bash scripts/firecracker-setup.sh collect api
```

The command prints a new private `results.XXXXXX/` directory under the instance:

- `repo.bundle`: Git history and branches, including any commits the task authorized.
- `changes.patch`: tracked staged/unstaged edits relative to the guest HEAD.
- `untracked.tar.gz`: non-ignored untracked files, including new source files.
- `base.txt`, `head.txt`, `status.txt`: initial source commit, collected HEAD and status.

Review these artifacts before importing. The patch does not contain committed
changes; retrieve those from the bundle. New untracked files are in the archive.
Ignored files and Docker volumes stay in the guest. Collection does not unpack,
apply or merge anything into the host checkout, and does not create commits.
Repeat collection produces another results directory rather than replacing the
previous one. Use a separate review branch/worktree when integrating workers'
changes; overlapping edits may require ordinary Git conflict resolution.

## Verification and current limits

The offline launcher test runs in `make test`; a focused invocation is:

```sh
make shell CMD='python3 scripts/firecracker-setup-test.py'
```

Before relying on this development path, verify setup, Docker container execution,
authenticated Codex, terminal reconnect and stop/start persistence on real
concurrent guests. Build a base once, then run `make check` and `make test-e2e`
inside clones. Offline tests
cannot establish guest compatibility or startup speed.

This version cold-boots a prepared disk and keeps Docker caches. It does not use
memory snapshots. A future snapshot path needs networking and wall-clock recovery;
in particular, Firecracker's `clock_realtime` restore option requires a TSC-based
host clock. See the [snapshot documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md).
Host kernels outside the [published matrix](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)
need an actual boot check. Do not infer full compatibility from a successful KVM
probe.
