#!/usr/bin/env bash
# Optional Linux development host. All application containers run inside the guest.
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
base=${FIRECRACKER_STATE_DIR:-$repo/.local/firecracker}
state=$base
name=almanac
jail=$state/jailer/firecracker/almanac/root
guest=192.168.241.2
gateway=192.168.241.1
subnet=192.168.241.0/30
tap=almfc0
chain=ALMANAC_FIRECRACKER
guest_mac=06:00:c0:a8:f1:02
prompt=

fail() { printf 'firecracker: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
Usage: ./firecracker-setup.sh ACTION [NAME] [--prompt TEXT|-] [--repo PATH]

  setup   Clone the prepared base (or install the first seed), then copy code/login.
  base    Warm a seed with make setup, stop it and publish a reusable CoW base.
  check   Check host prerequisites without changing anything.
  start   Boot the existing guest and wait for SSH.
  ssh     Open a shell as dev (starts the guest if necessary).
  codex   Attach to Codex in tmux (starts the guest if necessary).
  run     Prepare/start a named guest and launch Codex in background; needs --prompt.
  collect Retrieve commits, tracked edits and new files without applying them.
  list    List every prepared or partially prepared guest, running or stopped.
  stop    Shut down the guest and remove its network rules; preserve its disk.
  delete  Stop and permanently remove one explicitly named guest and its credentials.
  status  Report whether this guest is running.

Default action: setup. Unnamed setup/run/base generate a name like fuzzy-tiger.
Other lifecycle commands default to the original almanac guest; delete needs NAME.
Named guests live in ignored .local/firecracker/instances/NAME/.
Use --prompt - to read all of stdin, or --prompt "task" for an inline prompt.
--repo PATH selects the checkout to copy on first setup/run, including dirty files.
New named guests default to 2 CPUs, 4096 MiB RAM, 80 GiB sparse disk.
VM_CPUS, VM_MEMORY_MIB and VM_DISK_GIB override these defaults before setup.
Clones inherit the base disk size; refresh the base when dependencies change.
FIRECRACKER_STATE_DIR selects the shared private state/cache directory.
See docs/firecracker.md. Run as your normal user; privileged steps use sudo.
EOF
}

number() {
    local name=$1 value=$2 minimum=$3 maximum=$4
    [[ $value =~ ^[1-9][0-9]{0,5}$ ]] || fail "$name must be a decimal integer"
    (( value >= minimum && value <= maximum )) || fail "$name must be $minimum..$maximum"
}

select_instance() {
    name=$1
    [[ $name =~ ^[a-z][a-z0-9-]{0,31}$ ]] || fail 'NAME must be 1..32 lowercase letters, digits or hyphens, starting with a letter'
    state=$base
    if [[ $name != almanac ]]; then state=$base/instances/$name; fi
    [[ $state == "$(realpath -m -- "$state")" ]] || fail 'instance directory must not contain symlinks'
    jail=$state/jailer/firecracker/$name/root
    guest=192.168.241.2; gateway=192.168.241.1; subnet=192.168.241.0/30
    tap=almfc0; chain=ALMANAC_FIRECRACKER; guest_mac=06:00:c0:a8:f1:02
    if [[ -f $state/network-slot ]]; then
        local slot
        read -r slot < "$state/network-slot"
        use_slot "$slot"
    fi
}

reserve_random_instance() {
    local -a adjectives=(
        brave bright calm clever cosmic cozy curious dapper eager fancy fizzy fluffy
        friendly fuzzy gentle happy jolly lazy lively lucky mellow merry nimble
        playful quiet rapid sad silly sleepy sunny swift zesty
    ) animals=(
        alpaca badger bat bear beaver bison bobcat camel
        capybara cat cheetah cobra coyote crane deer dingo
        dolphin donkey eagle falcon ferret finch fox frog
        gecko giraffe goat goose hamster hare hedgehog heron
        hippo ibis iguana jaguar koala lemur leopard lion
        llama lynx marmot meerkat moose moth newt octopus
        otter owl panda parrot penguin puma quail rabbit
        raven seal sloth squirrel tiger turtle walrus zebra
    )
    [[ $base/instances == "$(realpath -m -- "$base/instances")" ]] \
        || fail 'instance directory must not contain symlinks'
    mkdir -p -- "$base/instances"
    local attempt candidate
    for ((attempt=0; attempt<128; attempt++)); do
        candidate=${adjectives[RANDOM % ${#adjectives[@]}]}-${animals[RANDOM % ${#animals[@]}]}
        # Atomic mkdir reserves the name across concurrent launchers.
        if mkdir -m 700 -- "$base/instances/$candidate" 2>/dev/null; then
            select_instance "$candidate"
            printf 'VM: %s\n' "$name"
            return
        fi
        [[ -e $base/instances/$candidate || -L $base/instances/$candidate ]] \
            || fail 'cannot create instance directory'
    done
    fail 'could not reserve a random VM name; supply NAME explicitly'
}

use_slot() {
    local slot=$1
    number network-slot "$slot" 1 1023
    (( slot != 64 )) || fail 'network slot 64 is reserved for the legacy guest'
    local prefix=192.168.$((240 + slot / 64)) offset=$((slot % 64 * 4))
    gateway=$prefix.$((offset + 1))
    guest=$prefix.$((offset + 2))
    subnet=$prefix.$offset/30
    tap=almfc$slot
    chain=ALMFC_$slot
    printf -v guest_mac '06:%02x:c0:a8:f0:%02x' "$((slot / 256))" "$((slot % 256))"
}

reserve_network() {
    [[ $name != almanac ]] || return 0
    (
        exec 8> "$base/network.lock"
        flock 8
        [[ ! -f $state/network-slot ]] || exit 0
        local path slot
        local -A used=()
        for path in "$base"/instances/*/network-slot; do
            [[ -f $path ]] || continue
            read -r slot < "$path"
            number network-slot "$slot" 1 1023
            used[$slot]=1
        done
        for ((slot=1; slot<=1023; slot++)); do
            (( slot != 64 )) || continue
            if [[ -z ${used[$slot]:-} ]]; then
                printf '%s\n' "$slot" > "$state/network-slot"
                exit 0
            fi
        done
        fail 'all 1022 named network slots are reserved; delete unused guests to release slots'
    )
    local slot
    read -r slot < "$state/network-slot"
    use_slot "$slot"
}

download() {
    (
    local url=$1 digest=$2 destination=$3
    exec 8> "$destination.lock"
    flock 8
    if [[ ! -e $destination ]]; then
        curl --fail --location --show-error --proto '=https' --tlsv1.2 \
            --connect-timeout 15 --max-time 600 --output "$destination.part" "$url"
        printf '%s  %s\n' "$digest" "$destination.part" | sha256sum --check --status \
            || fail "checksum mismatch: $destination.part (not installed)"
        mv -- "$destination.part" "$destination"
    fi
    printf '%s  %s\n' "$digest" "$destination" | sha256sum --check --status \
        || fail "checksum mismatch: $destination (remove this cached file to download again)"
    )
}

running() {
    [[ -f $jail/firecracker.pid ]] || return 1
    local pid='' executable='' expected=''
    # Jailer creates the PID file as root before dropping privileges.
    if [[ -r $jail/firecracker.pid ]]; then
        pid=$(cat -- "$jail/firecracker.pid") || fail "cannot read PID file: $jail/firecracker.pid; VM preserved"
    else
        pid=$(sudo cat -- "$jail/firecracker.pid") || fail "cannot read PID file: $jail/firecracker.pid; VM preserved"
    fi
    [[ $pid =~ ^[1-9][0-9]*$ ]] || fail "invalid PID in $jail/firecracker.pid; VM preserved"
    [[ -d /proc/$pid ]] || return 1
    if ! executable=$(stat -L -c '%d:%i' "/proc/$pid/exe" 2>/dev/null); then
        [[ ! -d /proc/$pid ]] && return 1
        fail "cannot inspect process $pid; VM preserved"
    fi
    expected=$(stat -L -c '%d:%i' "$jail/firecracker") || fail 'cannot identify jailed executable; VM preserved'
    # A jailed process may report /firecracker; compare identity rather than its path.
    [[ $executable == "$expected" ]]
}

check() {
    [[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'requires x86_64 Linux'
    (( EUID != 0 )) || fail 'run as your normal user, not root'
    local tool
    for tool in curl sha256sum tar unsquashfs mkfs.ext4 ssh ssh-keygen scp git \
        ip iptables sudo python3 flock realpath truncate install sed stat readlink mount umount; do
        command -v "$tool" >/dev/null || fail "missing host command: $tool (see docs/firecracker.md)"
    done
    [[ -r /dev/kvm && -w /dev/kvm ]] || fail '/dev/kvm must be readable and writable'
    [[ -e /dev/net/tun ]] || fail '/dev/net/tun is missing'
    [[ $(stat -fc %T /sys/fs/cgroup) == cgroup2fs ]] || fail 'requires cgroup v2'
    [[ $(cat /proc/sys/net/ipv4/ip_forward) == 1 ]] || fail 'IPv4 forwarding must be enabled before start'
    python3 - <<'PY'
import fcntl, os
fd = os.open('/dev/kvm', os.O_RDWR | os.O_CLOEXEC)
assert fcntl.ioctl(fd, 0xAE00, 0) == 12, 'unsupported KVM API'
vm = fcntl.ioctl(fd, 0xAE01, 0)
os.close(vm)
os.close(fd)
PY
    printf 'Host prerequisites passed. Network/image preparation will require sudo.\n'
}

ssh_guest() { ssh -F "$state/ssh_config" "$@"; }

network_up() {
    # Never reuse an existing interface or replace an operator's firewall policy.
    ! ip link show "$tap" >/dev/null 2>&1 || fail "$tap already exists; stop its VM first"
    python3 - "$subnet" <<'PY'
import ipaddress, json, subprocess, sys
network = ipaddress.ip_network(sys.argv[1])
routes = json.loads(subprocess.check_output(['ip', '-j', '-4', 'route', 'show', 'table', 'all']))
for route in routes:
    dst = route.get('dst', 'default')
    if dst != 'default' and network.overlaps(ipaddress.ip_network(dst, strict=False)):
        sys.exit(f'Guest subnet {network} overlaps host route {dst}; no network changes made')
PY
    sudo ip tuntap add dev "$tap" mode tap user "$(id -u)"
    sudo ip link set dev "$tap" alias "almanac-firecracker:$state"
    sudo ip addr add "$gateway/30" dev "$tap"
    sudo ip link set dev "$tap" up
    sudo iptables -w -N "$chain"
    # Guests may reach the public internet, but cannot initiate connections to the host/LAN.
    local range
    for range in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 \
        169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4; do
        sudo iptables -w -A "$chain" -d "$range" -j REJECT
    done
    sudo iptables -w -A "$chain" -s "$guest/32" -j ACCEPT
    sudo iptables -w -A "$chain" -j DROP
    sudo iptables -w -I FORWARD 1 -i "$tap" -j "$chain"
    sudo iptables -w -I FORWARD 1 -o "$tap" -d "$guest/32" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    sudo iptables -w -I INPUT 1 -i "$tap" -s "$guest/32" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    sudo iptables -w -I INPUT 2 -i "$tap" -j REJECT
    sudo iptables -w -t nat -A POSTROUTING -s "$guest/32" ! -o "$tap" -j MASQUERADE
}

network_down() {
    local alias
    [[ -e /sys/class/net/$tap/ifalias ]] || return 0
    read -r alias < "/sys/class/net/$tap/ifalias" || true
    [[ $alias == "almanac-firecracker:$state" ]] || fail "refusing to remove another workspace's $tap"
    sudo -v
    # A failed start can leave only some of these exact, owned rules installed.
    remove_rule filter FORWARD -i "$tap" -j "$chain"
    remove_rule filter FORWARD -o "$tap" -d "$guest/32" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    remove_rule filter INPUT -i "$tap" -s "$guest/32" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    remove_rule filter INPUT -i "$tap" -j REJECT
    remove_rule nat POSTROUTING -s "$guest/32" ! -o "$tap" -j MASQUERADE
    if sudo iptables -w -S "$chain" >/dev/null 2>&1; then
        sudo iptables -w -F "$chain"
        sudo iptables -w -X "$chain"
    fi
    sudo ip link del "$tap"
}

remove_rule() {
    local table=$1
    shift
    if sudo iptables -w -t "$table" -C "$@" 2>/dev/null; then
        sudo iptables -w -t "$table" -D "$@"
    fi
}

start() {
    [[ -f $jail/vm.json ]] || fail 'run setup first'
    if ! running; then
        sudo -v
        network_down
        # Jailer recreates its device nodes on every launch and rejects existing ones.
        rm -f -- "$jail/api.socket" "$jail/firecracker.pid" \
            "$jail/dev/net/tun" "$jail/dev/kvm" "$jail/dev/urandom" "$jail/dev/userfaultfd"
        network_up
        sudo "$state/bin/jailer" --id "$name" --exec-file "$state/bin/firecracker" \
            --uid "$(id -u)" --gid "$(id -g)" --chroot-base-dir "$state/jailer" \
            --cgroup-version 2 --new-pid-ns --daemonize \
            -- --config-file /vm.json --api-sock /api.socket --log-path /firecracker.log --level Info
        # Let later read-only status/list commands inspect the PID without another sudo login.
        sudo chown -h "$(id -u):$(id -g)" "$jail/firecracker.pid"
    fi
    local attempt
    for (( attempt=0; attempt<60; attempt++ )); do
        if ssh_guest -o ConnectTimeout=1 -o ConnectionAttempts=1 root true 2>/dev/null; then
            printf 'Guest SSH ready.\n'
            return
        fi
        running || fail "VM exited; inspect $jail/firecracker.log"
        sleep 1
    done
    fail "guest SSH did not become ready; VM preserved for inspection at $jail"
}

stop() {
    if running; then
        # On x86, reboot=k exits the VMM after orderly shutdown; poweroff cannot exit it.
        # Talk directly to PID 1; minimal images need not run D-Bus/logind.
        ssh_guest -o ConnectTimeout=3 root \
            'systemctl --system --no-block --job-mode=replace-irreversibly start reboot.target' \
            || fail 'shutdown request failed; guest preserved'
        local attempt
        for (( attempt=0; attempt<60; attempt++ )); do
            if ! running; then break; fi
            sleep 1
        done
        ! running || fail 'guest is still shutting down; disk and network preserved (no forced kill)'
    fi
    if [[ -d $state ]]; then network_down; fi
    printf 'Guest stopped; disk and credentials preserved.\n'
}

list_instances() (
    printf '%-32s %-10s %s\n' NAME STATE IP
    local path label
    local -a names=()
    if [[ -f $base/image-prepared || -f $base/ready || -d $base/jailer/firecracker/almanac/root ]]; then names+=(almanac); fi
    for path in "$base"/instances/*; do
        [[ -d $path ]] || continue
        names+=("${path##*/}")
    done
    for label in "${names[@]}"; do
        select_instance "$label"
        if running; then label=running
        elif [[ -f $state/ready ]]; then label=stopped
        else label=partial; fi
        printf '%-32s %-10s %s\n' "$name" "$label" "$guest"
    done
)

delete_instance() {
    [[ -d $state ]] || { printf '%s: no guest to delete.\n' "$name"; return; }
    # A failed offline edit may have left a loop mount: never traverse it during deletion.
    python3 - "$state" <<'PY'
from pathlib import Path
import re, sys
state = Path(sys.argv[1])
for line in open('/proc/self/mountinfo'):
    mount = re.sub(r'\\([0-7]{3})', lambda m: chr(int(m[1], 8)), line.split()[4])
    if Path(mount).is_relative_to(state):
        sys.exit(f'Guest state still contains a mounted filesystem: {mount}; unmount before deleting')
PY
    stop
    if [[ $name == almanac ]]; then
        # The legacy guest shares its parent directory with downloads, bases and named guests.
        sudo rm -rf -- "$jail" "$state/bin" "$state/build"
        rm -f -- "$state/id_ed25519" "$state/id_ed25519.pub" "$state/guest-host-key.pub" \
            "$state/known_hosts" "$state/ssh_config" "$state/image-prepared" "$state/provisioned" \
            "$state/checkout-copied" "$state/ready" "$state/repo.bundle" "$state/repo-head" \
            "$state/checkout.tar.gz" "$state/git-config.json"
    else
        local removed
        removed=$(mktemp -d "$base/deleted-$name.XXXXXX")
        mv -T -- "$state" "$removed"
        sudo rm -rf -- "$removed"
    fi
    printf '%s: guest disk and credentials deleted.\n' "$name"
}

prepare_network() {
    local rootfs=$1
    sudo ln -sfn /dev/null "$rootfs/etc/systemd/system/fcnet.service"
    sudo install -d "$rootfs/etc/systemd/network"
    sudo tee "$rootfs/etc/systemd/network/10-almanac.network" >/dev/null <<EOF
[Match]
Name=eth0
[Network]
Address=$guest/30
Gateway=$gateway
LinkLocalAddressing=no
IPv6AcceptRA=no
EOF
    printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' | sudo tee "$rootfs/etc/resolv.conf" >/dev/null
    # networkd runs unprivileged; guest users also need to read DNS configuration.
    sudo chmod 644 "$rootfs/etc/systemd/network/10-almanac.network" "$rootfs/etc/resolv.conf"
    sudo ln -sfn /usr/lib/systemd/system/systemd-networkd.service \
        "$rootfs/etc/systemd/system/multi-user.target.wants/systemd-networkd.service"
}

prepare_image() {
    local assets=$base/downloads build=$state/build rootfs=$state/build/rootfs
    [[ ! -e $jail/rootfs.ext4 && ! -e $state/id_ed25519 && ! -e $build ]] \
        || fail 'partial image/key/build already exists; inspect it before retrying (nothing overwritten)'
    mkdir -p "$assets" "$state/bin"
    download https://github.com/firecracker-microvm/firecracker/releases/download/v1.18.0/firecracker-v1.18.0-x86_64.tgz \
        1ac8eec866dd5eea14efecf6323e22b23eea24054545338a5b34bc5bca6623cd "$assets/firecracker.tgz"
    download https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20261105-6ea6634994fa-0/x86_64/vmlinux-6.18.56 \
        af348226aa2a25b846156150d5cdb5dae3bf2a022f1db176e3a2b9a3249817bf "$assets/vmlinux"
    download https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20261105-6ea6634994fa-0/x86_64/ubuntu-24.04.squashfs \
        91da33c2013bd1b018f2c1e6b3d37a3316e0ff8c19a71de640d7a4c3ca31b92b "$assets/ubuntu.squashfs"
    mkdir "$build"
    tar -xzf "$assets/firecracker.tgz" -C "$build"
    install -m 700 "$build/release-v1.18.0-x86_64/firecracker-v1.18.0-x86_64" "$state/bin/firecracker"
    install -m 700 "$build/release-v1.18.0-x86_64/jailer-v1.18.0-x86_64" "$state/bin/jailer"
    ssh-keygen -q -t ed25519 -N '' -f "$state/id_ed25519"
    sudo unsquashfs -no-progress -d "$rootfs" "$assets/ubuntu.squashfs"
    sudo install -d -m 700 "$rootfs/root/.ssh"
    sudo install -m 600 "$state/id_ed25519.pub" "$rootfs/root/.ssh/authorized_keys"
    # Replace the CI image's networking, test SSH keys and empty root password.
    sudo rm -f "$rootfs/etc/ssh/ssh_host_"* "$rootfs/etc/resolv.conf"
    sudo ssh-keygen -q -t ed25519 -N '' -f "$rootfs/etc/ssh/ssh_host_ed25519_key"
    sudo sed -i 's/^root:[^:]*:/root:!:/' "$rootfs/etc/shadow"
    sudo tee "$rootfs/etc/ssh/sshd_config" >/dev/null <<'EOF'
Port 22
HostKey /etc/ssh/ssh_host_ed25519_key
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
PubkeyAuthentication yes
UsePAM yes
AllowUsers root dev
Subsystem sftp internal-sftp
EOF
    prepare_network "$rootfs"
    sudo cat "$rootfs/etc/ssh/ssh_host_ed25519_key.pub" > "$state/guest-host-key.pub"
    awk -v host="$guest" '{print host, $1, $2}' "$state/guest-host-key.pub" > "$state/known_hosts"
    mkdir -p "$jail"
    cp "$assets/vmlinux" "$jail/vmlinux"
    truncate -s "${VM_DISK_GIB:-80}G" "$jail/rootfs.ext4"
    sudo mkfs.ext4 -q -F -d "$rootfs" "$jail/rootfs.ext4"
    sudo chown "$(id -u):$(id -g)" "$jail/rootfs.ext4"
    configure_instance
    # Only the completed disk is needed after mkfs; preserve failed builds for diagnosis.
    sudo rm -rf -- "$build"
}

configure_instance() {
    awk -v host="$guest" '{print host, $1, $2}' "$state/guest-host-key.pub" > "$state/known_hosts"
    python3 - "$jail/vm.json" "$VM_CPUS" "$VM_MEMORY_MIB" "$tap" "$guest_mac" <<'PY'
import json, sys
path, cpus, memory, tap, mac = sys.argv[1:]
with open(path, 'w') as f:
    json.dump({
        'boot-source': {'kernel_image_path': '/vmlinux', 'boot_args':
            'console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw net.ifnames=0 ipv6.disable=1'},
        'drives': [{'drive_id': 'rootfs', 'path_on_host': '/rootfs.ext4',
                    'is_root_device': True, 'is_read_only': False}],
        'machine-config': {'vcpu_count': int(cpus), 'mem_size_mib': int(memory)},
        'network-interfaces': [{'iface_id': 'eth0', 'host_dev_name': tap,
                                'guest_mac': mac}],
    }, f, indent=2)
PY
    cat > "$state/ssh_config" <<EOF
Host root dev
    HostName $guest
    IdentityFile "$state/id_ed25519"
    IdentitiesOnly yes
    UserKnownHostsFile "$state/known_hosts"
    StrictHostKeyChecking yes
    BatchMode yes
    ServerAliveInterval 15
    ForwardAgent no
Host root
    User root
Host dev
    User dev
EOF
}

dependency_fingerprint() {
    local -a command=(python3 - "$repo")
    if [[ $1 == guest ]]; then
        command=(ssh -F "$state/ssh_config" dev python3 - /home/dev/almanac/.worktrees/codex)
    fi
    "${command[@]}" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
digest = hashlib.sha256()
for name in ('Makefile', 'compose.dev.yaml', 'deploy/dev/Dockerfile', '.dockerignore',
             'go.mod', 'go.sum', 'web/package.json', 'web/package-lock.json',
             'tools/openapi/package.json', 'tools/openapi/package-lock.json'):
    path = root / name
    digest.update(name.encode() + b'\0')
    data = path.read_bytes() if path.exists() else b'<absent>'
    if name == 'Makefile':
        # Lifecycle/test target changes do not invalidate development dependencies.
        lines = data.splitlines(keepends=True)
        start = lines.index(b'setup:\n') + 1
        recipe = []
        for line in lines[start:]:
            if not line.startswith(b'\t'):
                break
            recipe.append(line)
        data = b''.join(recipe)
    digest.update(data)
    digest.update(b'\0')
print(digest.hexdigest())
PY
}

validate_image_paths() {
    # Never follow guest-controlled directory symlinks into the host during offline edits.
    sudo python3 - "$1" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1]).resolve(strict=True)
for name in ('home', 'root', 'tmp', 'var', 'var/tmp', 'var/log', 'var/lib',
             'var/lib/dbus', 'var/lib/systemd', 'var/lib/docker',
             'var/lib/docker/network', 'var/lib/docker/network/files',
             'etc', 'etc/ssh', 'etc/systemd', 'etc/systemd/network',
             'etc/systemd/system', 'etc/systemd/system/multi-user.target.wants'):
    path = root / name
    if path.is_symlink() or not path.resolve().is_relative_to(root):
        sys.exit(f'Refusing guest directory symlink: {name}')
PY
}

sanitize_image() {
    local rootfs=$1
    validate_image_paths "$rootfs"
    sudo python3 - "$rootfs" <<'PY'
from pathlib import Path
import shutil, sys
root = Path(sys.argv[1])
for name, mode in (('home', 0o755), ('root', 0o700), ('tmp', 0o1777),
                   ('var/tmp', 0o1777), ('var/log', 0o755)):
    path = root / name
    if path.exists():
        shutil.rmtree(path)
    path.mkdir(mode=mode, parents=True)
    path.chmod(mode)
for path in (root / 'etc/ssh').glob('ssh_host_*'):
    path.unlink()
for name in ('etc/machine-id', 'var/lib/dbus/machine-id', 'var/lib/systemd/random-seed',
             'var/lib/docker/engine-id', 'var/lib/docker/key.json',
             'var/lib/docker/network/files/local-kv.db'):
    (root / name).unlink(missing_ok=True)
(root / 'etc/machine-id').write_text('')
(root / 'etc/machine-id').chmod(0o644)
PY
}

edit_image() (
    local disk=$1 operation=$2 rootfs
    rootfs=$(mktemp -d "$state/mount.XXXXXX")
    # If unmount fails, keep the directory and image for diagnosis.
    trap 'if sudo umount -- "$rootfs"; then rmdir -- "$rootfs"; fi' EXIT
    sudo mount -t ext4 -o loop,nodev,nosuid,noexec -- "$disk" "$rootfs"
    "$operation" "$rootfs"
    sudo umount -- "$rootfs"
    trap - EXIT
    rmdir -- "$rootfs"
)

initialize_clone() {
    local rootfs=$1 uid gid
    validate_image_paths "$rootfs"
    read -r uid gid < <(sudo python3 - "$rootfs/etc/passwd" <<'PY'
import sys
for line in open(sys.argv[1]):
    fields = line.rstrip().split(':')
    if fields[0] == 'dev':
        print(fields[2], fields[3])
        break
else:
    sys.exit('Base image has no dev user')
PY
    )
    [[ $uid =~ ^[0-9]+$ && $gid =~ ^[0-9]+$ ]] || fail 'invalid dev identity in base'
    sudo install -d -m 700 "$rootfs/root/.ssh"
    sudo install -d -m 700 -o "$uid" -g "$gid" "$rootfs/home/dev" \
        "$rootfs/home/dev/.ssh" "$rootfs/home/dev/.codex"
    sudo install -m 600 "$state/id_ed25519.pub" "$rootfs/root/.ssh/authorized_keys"
    sudo install -m 600 -o "$uid" -g "$gid" "$state/id_ed25519.pub" "$rootfs/home/dev/.ssh/authorized_keys"
    printf 'cli_auth_credentials_store = "file"\n' | sudo tee "$rootfs/home/dev/.codex/config.toml" >/dev/null
    sudo chown "$uid:$gid" "$rootfs/home/dev/.codex/config.toml"
    sudo ssh-keygen -q -t ed25519 -N '' -f "$rootfs/etc/ssh/ssh_host_ed25519_key"
    sudo cat "$rootfs/etc/ssh/ssh_host_ed25519_key.pub" > "$state/guest-host-key.pub"
    # These files are recreated rather than following possible guest symlinks.
    sudo rm -f -- "$rootfs/etc/hostname" "$rootfs/etc/hosts" "$rootfs/etc/resolv.conf" \
        "$rootfs/etc/systemd/network/10-almanac.network"
    printf '%s\n' "$name" | sudo tee "$rootfs/etc/hostname" >/dev/null
    printf '127.0.0.1 localhost\n127.0.1.1 %s\n' "$name" | sudo tee "$rootfs/etc/hosts" >/dev/null
    sudo chmod 644 "$rootfs/etc/hostname" "$rootfs/etc/hosts"
    prepare_network "$rootfs"
}

clone_image() {
    local image
    image=$(realpath -e -- "$base/base-image")
    [[ $(dependency_fingerprint host) == "$(cat "$image/dependencies.sha256")" ]] \
        || fail 'dependencies differ from the base; refresh it with base --repo PATH'
    if [[ -n ${VM_DISK_GIB:-} ]]; then
        [[ $(stat -c %s "$image/rootfs.ext4") == "$((VM_DISK_GIB * 1024 * 1024 * 1024))" ]] \
            || fail 'VM_DISK_GIB must match the base size for clones; rebuild the base to change it'
    fi
    [[ ! -e $jail/rootfs.ext4 && ! -e $state/id_ed25519 ]] \
        || fail 'partial image/key already exists; inspect it before retrying (nothing overwritten)'
    mkdir -p "$jail" "$state/bin"
    # Fail instead of silently doing a slow full copy on a filesystem without reflinks.
    cp --reflink=always -- "$image/rootfs.ext4" "$jail/rootfs.ext4" \
        || fail 'CoW clone failed; base and instances must share a reflink-capable filesystem'
    chmod 600 "$jail/rootfs.ext4"
    cp -- "$image/vmlinux" "$jail/vmlinux"
    install -m 700 "$image/firecracker" "$state/bin/firecracker"
    install -m 700 "$image/jailer" "$state/bin/jailer"
    ssh-keygen -q -t ed25519 -N '' -f "$state/id_ed25519"
    edit_image "$jail/rootfs.ext4" initialize_clone
    configure_instance
    printf 'Cloned prepared base; Docker images and dependency caches inherited.\n'
}

provision() {
    ssh_guest root bash -s <<'GUEST'
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl gnupg git gh make sudo tmux rsync systemd-timesyncd
install -m 0755 -d /etc/apt/keyrings
curl --fail --show-error --location https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
gpg --show-keys --with-colons /etc/apt/keyrings/docker.asc | grep -q '^fpr:::::::::9DC858229FC7DD38854AE2D88D81803C0EBFCD88:'
printf '%s\n' 'deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable' > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker systemd-timesyncd
if ! id dev >/dev/null 2>&1; then useradd --create-home --shell /bin/bash dev; fi
usermod -aG docker,sudo dev
printf 'dev ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/almanac-dev
chmod 440 /etc/sudoers.d/almanac-dev
install -d -m 700 -o dev -g dev /home/dev/.ssh /home/dev/.codex
install -m 600 -o dev -g dev /root/.ssh/authorized_keys /home/dev/.ssh/authorized_keys
printf 'cli_auth_credentials_store = "file"\n' > /home/dev/.codex/config.toml
chown dev:dev /home/dev/.codex/config.toml
GUEST
    install_codex
}

install_codex() {
    ssh_guest root bash -s <<'GUEST'
set -euo pipefail
install -d -m 755 /opt/codex
runtime=$(mktemp -d /opt/codex/0.167.0.XXXXXX)
curl --fail --show-error --location --proto '=https' --tlsv1.2 \
    --connect-timeout 15 --max-time 600 \
    https://github.com/openai/codex/releases/download/rust-v0.167.0/codex-package-x86_64-unknown-linux-musl.tar.gz \
    -o "$runtime/package.tar.gz"
printf '%s  %s\n' 3bbf75e83de6b58aed28f2cf6e33ad423ec31fd4700a1457b94be557ea5a7c6e "$runtime/package.tar.gz" | sha256sum --check --status
tar -xzf "$runtime/package.tar.gz" -C "$runtime"
for binary in codex codex-code-mode-host codex-exec-server; do
    test -x "$runtime/bin/$binary"
done
chmod 755 "$runtime"
for binary in codex codex-code-mode-host codex-exec-server; do
    ln -sfn "$runtime/bin/$binary" "/usr/local/bin/$binary"
done
rm "$runtime/package.tar.gz"
codex --version
GUEST
}

copy_checkout() {
    # The bundle gives the guest its own Git database, including linked-worktree sources.
    git -C "$repo" bundle create "$state/repo.bundle" --all
    git -C "$repo" rev-parse HEAD > "$state/repo-head"
    (
        cd "$repo"
        git ls-files --cached --others --exclude-standard -z |
            while IFS= read -r -d '' path; do
                if [[ -e $path || -L $path ]]; then printf '%s\0' "$path"; fi
            done |
            tar --null --verbatim-files-from --no-recursion -T - -czf "$state/checkout.tar.gz"
    )
    python3 - "$repo" "$name" > "$state/git-config.json" <<'PY'
import json, re, subprocess, sys
from urllib.parse import urlsplit
repo, name = sys.argv[1:]
def git(*args):
    result = subprocess.run(['git', '-C', repo, *args], capture_output=True, text=True)
    return result.stdout.strip() if result.returncode == 0 else ''
url = git('remote', 'get-url', 'origin')
path = ''
if url.startswith('git@github.com:'):
    path = url.removeprefix('git@github.com:')
elif url.startswith(('https://', 'ssh://')):
    parsed = urlsplit(url)
    if parsed.hostname == 'github.com':
        path = parsed.path.lstrip('/')
path = path.rstrip('/').removesuffix('.git')
remote = f'https://github.com/{path}.git' if re.fullmatch(r'[\w.-]+/[\w.-]+', path, re.ASCII) else ''
json.dump({'name': git('config', 'user.name'), 'email': git('config', 'user.email'),
           'remote': remote, 'branch': f'codex/{name}'}, sys.stdout)
PY
    scp -F "$state/ssh_config" "$state/repo.bundle" "$state/repo-head" "$state/checkout.tar.gz" "$state/git-config.json" dev:/home/dev/
    ssh_guest dev bash -s <<'GUEST'
set -euo pipefail
test ! -e /home/dev/almanac || { echo 'Guest checkout exists; refusing to overwrite it' >&2; exit 1; }
git clone /home/dev/repo.bundle /home/dev/almanac
cd /home/dev/almanac
git checkout --detach "$(cat /home/dev/repo-head)"
# This is a new clone: replace its tracked files with the exact current working tree.
git ls-files -z | xargs -0 -r rm -f --
tar -xzf /home/dev/checkout.tar.gz
branch=$(python3 -c 'import json; print(json.load(open("/home/dev/git-config.json"))["branch"])')
base_branch=$branch
suffix=0
while git show-ref --verify --quiet "refs/heads/$branch"; do
    suffix=$((suffix + 1))
    branch=$base_branch-$suffix
done
git worktree add .worktrees/codex -b "$branch" HEAD
# Carry the supplied uncommitted files into the dedicated task worktree as well.
git -C .worktrees/codex ls-files -z | (cd .worktrees/codex && xargs -0 -r rm -f --)
tar -xzf /home/dev/checkout.tar.gz -C .worktrees/codex
git remote remove origin
python3 - <<'PY'
import json, subprocess
config = json.load(open('/home/dev/git-config.json'))
for key in ('name', 'email'):
    if config[key]:
        subprocess.run(['git', 'config', f'user.{key}', config[key]], check=True)
if config['remote']:
    subprocess.run(['git', 'remote', 'add', 'origin', config['remote']], check=True)
PY
rm /home/dev/repo.bundle /home/dev/repo-head /home/dev/checkout.tar.gz /home/dev/git-config.json
GUEST
}

ensure_github_cli() {
    ssh_guest root bash -s <<'GUEST'
set -euo pipefail
if ! command -v gh >/dev/null; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends gh
fi
GUEST
}

copy_github_auth() {
    if ! command -v gh >/dev/null || ! gh auth token --hostname github.com >/dev/null 2>&1; then
        printf 'No host GitHub login available. Inside the guest run: gh auth login --hostname github.com\n'
        return
    fi
    ensure_github_cli
    # Keep tokens off argv, host artifacts and logs; only send them through encrypted stdin.
    gh auth token --hostname github.com | ssh_guest dev 'umask 077
install -d -m 700 /home/dev/.config/gh
gh auth login --hostname github.com --git-protocol https --with-token
gh auth setup-git --hostname github.com'
}

prepare_base() (
    exec 8> "$base/base-image.lock"
    flock -n 8 || fail 'another base build is running'
    [[ -f $state/ready ]] || setup seed
    start
    [[ $(dependency_fingerprint host) == "$(dependency_fingerprint guest)" ]] \
        || fail 'seed dependencies differ from the requested checkout; use base with a new seed name'
    ssh_guest dev bash -s <<'GUEST'
set -euo pipefail
if tmux list-sessions >/dev/null 2>&1; then
    echo 'Close terminal/tmux tasks in this seed before sealing a base; seed preserved' >&2
    exit 1
fi
# Only dependency caches may enter the base; refuse a seed used for application data.
for container in $(docker container ls -aq); do
    test "$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$container")" = almanac-dev || {
        echo 'Seed has non-development containers; use a clean seed for the base' >&2; exit 1;
    }
done
for volume in $(docker volume ls -q); do
    case "$volume" in
        almanac-dev_go-mod|almanac-dev_go-build|almanac-dev_npm-cache|almanac-dev_web-node-modules|almanac-dev_openapi-node-modules|almanac-dev_web-test-results) ;;
        *) echo 'Seed has non-development volumes; use a clean seed for the base' >&2; exit 1 ;;
    esac
done
cd /home/dev/almanac/.worktrees/codex
make setup
make stop-dev
test -z "$(docker container ls -aq)"
GUEST
    ensure_github_cli
    local image fingerprint
    fingerprint=$(dependency_fingerprint guest)
    stop
    mkdir -p "$base/base-images"
    image=$(mktemp -d "$base/base-images/image.XXXXXX")
    cp --reflink=always -- "$jail/rootfs.ext4" "$image/rootfs.ext4" \
        || fail 'CoW copy failed; base and instances must share a reflink-capable filesystem'
    edit_image "$image/rootfs.ext4" sanitize_image
    cp -- "$jail/vmlinux" "$image/vmlinux"
    cp -- "$state/bin/firecracker" "$state/bin/jailer" "$image/"
    printf '%s\n' "$fingerprint" > "$image/dependencies.sha256"
    chmod 400 "$image/rootfs.ext4" "$image/vmlinux" "$image/dependencies.sha256"
    chmod 500 "$image/firecracker" "$image/jailer"
    # Publishing one pointer keeps concurrent clones on a complete immutable generation.
    ln -s -- "$image" "$image/current"
    mv -Tf -- "$image/current" "$base/base-image"
    printf 'Prepared base: %s\nSeed %s is stopped and preserved. New workers skip make setup.\n' "$image" "$name"
)

setup() {
    if [[ -f $state/ready ]]; then
        printf 'Guest already prepared; its checkout, disk and credentials were preserved.\n'
        return
    fi
    [[ -e $base/base-image || ${1:-} == seed ]] \
        || fail 'prepare shared dependencies first: ./firecracker-setup.sh base --repo PATH'
    if [[ ! -f $state/image-prepared ]]; then
        if [[ -e $base/base-image && ${1:-} != seed ]]; then
            clone_image
            touch "$state/provisioned"
        else
            prepare_image
        fi
        touch "$state/image-prepared"
    fi
    start
    if [[ ! -f $state/provisioned ]]; then
        provision
        touch "$state/provisioned"
    fi
    if [[ ! -f $state/checkout-copied ]]; then
        copy_checkout
        touch "$state/checkout-copied"
    fi
    if [[ ${1:-} != seed ]]; then
        local auth=${CODEX_HOME:-$HOME/.codex}/auth.json
        if [[ -s $auth ]]; then
            # Copy once, never replace guest credentials that may have refreshed independently.
            if ! ssh_guest dev test -s /home/dev/.codex/auth.json; then
                ssh_guest dev 'umask 077; cat > /home/dev/.codex/auth.json' < "$auth"
            fi
            ssh_guest dev codex login status
        else
            printf 'No file-based host login; inside the guest run: codex login --device-auth\n'
        fi
        copy_github_auth
    fi
    ssh_guest dev 'docker info --format "Guest Docker: {{.ServerVersion}}" && docker compose version && codex --version'
    touch "$state/ready"
    printf 'Setup prepared. Run ./firecracker-setup.sh codex %s (or ssh).\n' "$name"
    if [[ ! -e $base/base-image ]]; then
        printf 'Prepare shared dependencies once: ./firecracker-setup.sh base %s\n' "$name"
    fi
}

launch_agent() {
    local session_status=0
    ssh_guest dev 'tmux has-session -t codex 2>/dev/null' || session_status=$?
    case "$session_status" in
        0)
            [[ -z $prompt ]] || fail 'Codex session already exists; attach with codex NAME instead of replacing its prompt'
            return ;;
        1) ;;
        *) fail 'could not inspect Codex session; existing prompt preserved' ;;
    esac
    printf '%s' "$prompt" | ssh_guest dev 'umask 077; cat > /home/dev/.codex/almanac-prompt.txt'
    ssh_guest dev bash -s <<'GUEST'
set -euo pipefail
cat > /home/dev/.codex/almanac-run <<'RUNNER'
#!/usr/bin/env bash
set -euo pipefail
args=()
if [[ -s /home/dev/.codex/almanac-prompt.txt ]]; then
    IFS= read -r -d '' prompt < /home/dev/.codex/almanac-prompt.txt || true
    args=(-- "$prompt")
fi
exec codex --sandbox danger-full-access --ask-for-approval never \
    -c 'projects."/home/dev/almanac".trust_level="trusted"' "${args[@]}"
RUNNER
chmod 700 /home/dev/.codex/almanac-run
tmux new-session -d -s codex -c /home/dev/almanac/.worktrees/codex /home/dev/.codex/almanac-run
GUEST
}

collect() {
    ssh_guest dev bash -s <<'GUEST'
set -euo pipefail
if tmux has-session -t codex 2>/dev/null; then
    echo 'Quit Codex in its terminal before collecting; the agent may still be editing files' >&2
    exit 1
fi
mkdir -p /home/dev/.codex/almanac-export
cd /home/dev/almanac/.worktrees/codex
git bundle create /home/dev/.codex/almanac-export/repo.bundle --all
git diff --binary HEAD > /home/dev/.codex/almanac-export/changes.patch
git ls-files --others --exclude-standard -z |
    tar --null --verbatim-files-from --no-recursion -T - -czf /home/dev/.codex/almanac-export/untracked.tar.gz
git status --short > /home/dev/.codex/almanac-export/status.txt
git rev-parse HEAD > /home/dev/.codex/almanac-export/head.txt
GUEST
    local results
    results=$(mktemp -d "$state/results.XXXXXX")
    local file
    for file in repo.bundle changes.patch untracked.tar.gz status.txt head.txt; do
        scp -F "$state/ssh_config" "dev:/home/dev/.codex/almanac-export/$file" "$results/"
    done
    cp "$state/repo-head" "$results/base.txt"
    printf 'Collected results in %s (nothing applied to the host checkout).\n' "$results"
}

main() {
    local action=${1:-setup}
    if (( $# )); then shift; fi
    local name_given=0
    if (( $# )) && [[ $1 != --* ]]; then name=$1; name_given=1; shift; fi
    local source=$repo prompt_given=0
    while (( $# )); do
        case "$1" in
            --prompt)
                (( $# >= 2 && prompt_given == 0 )) || fail '--prompt needs one value and may appear only once'
                prompt_given=1
                if [[ $2 == - ]]; then
                    if [[ -t 0 ]]; then
                        printf 'Enter the task, then press Enter followed by Ctrl-D to launch (Ctrl-C cancels):\n' >&2
                    fi
                    if IFS= read -r -d '' prompt; then fail 'prompt must be text without NUL bytes'; fi
                else prompt=$2; fi
                [[ $prompt =~ [^[:space:]] ]] || fail 'prompt must not be empty'
                shift 2 ;;
            --repo)
                (( $# >= 2 )) || fail '--repo needs a checkout path'
                source=$(cd -- "$2" && pwd -P)
                [[ $(git -C "$source" rev-parse --show-toplevel) == "$source" ]] || fail '--repo must be a Git checkout root'
                shift 2 ;;
            *) fail "unknown option: $1 (see --help)" ;;
        esac
    done
    case "$action" in
        -h|--help) usage; return ;;
        setup|base|check|start|ssh|codex|run|collect|list|stop|delete|status) ;;
        *) fail "unknown action: $action (see --help)" ;;
    esac
    [[ $action == run || $action == codex || $prompt_given == 0 ]] || fail '--prompt is only valid with run or codex'
    [[ $action != run || $prompt_given == 1 ]] || fail 'run requires --prompt TEXT or --prompt -'
    [[ $action != delete || $name_given == 1 ]] || fail 'delete requires an explicit NAME; guest disks are permanently removed'
    [[ $source == "$repo" || $action == setup || $action == run || $action == base ]] || fail '--repo is only valid for first setup/run/base'
    [[ $base == /* && $base != *$'\n'* && $base != *'"'* ]] || fail 'FIRECRACKER_STATE_DIR must be an absolute path without quotes or newlines'
    [[ $base == "$(realpath -m -- "$base")" ]] || fail 'state path must be canonical, without symlinks or ..'
    if [[ $action == list ]]; then list_instances; return; fi
    local auto_name=0
    if [[ $name_given == 0 && ( $action == setup || $action == run || $action == base ) ]]; then
        auto_name=1
    else
        select_instance "$name"
    fi
    repo=$source
    if [[ $action == status ]]; then
        if running; then printf '%s: Guest running.\n' "$name"; else printf '%s: Guest stopped.\n' "$name"; fi
        return
    fi
    if [[ $name == almanac && $auto_name == 0 ]]; then
        VM_CPUS=${VM_CPUS:-4}; VM_MEMORY_MIB=${VM_MEMORY_MIB:-8192}
    else
        VM_CPUS=${VM_CPUS:-2}; VM_MEMORY_MIB=${VM_MEMORY_MIB:-4096}
    fi
    number VM_CPUS "$VM_CPUS" 1 32
    number VM_MEMORY_MIB "$VM_MEMORY_MIB" 2048 65536
    number VM_DISK_GIB "${VM_DISK_GIB:-80}" 16 1024
    if [[ ( $action == stop || $action == delete ) && ! -d $state ]]; then printf 'Guest absent/stopped.\n'; return; fi
    if [[ $action == check ]]; then check; return; fi
    if [[ $action != stop && $action != delete ]]; then check; fi
    umask 077
    if (( auto_name )); then reserve_random_instance; fi
    mkdir -p "$state"
    chmod 700 "$state"
    exec 9> "$state/lock"
    flock -n 9 || fail 'another launcher command is running'
    if [[ $action == setup || $action == run || $action == base ]]; then reserve_network; fi
    case "$action" in
        setup) setup ;;
        base) prepare_base ;;
        start) start ;;
        stop) stop ;;
        delete) delete_instance ;;
        run)
            setup
            start
            launch_agent
            printf '%s: Codex started in background. Attach: ./firecracker-setup.sh codex %s\n' "$name" "$name"
            ;;
        collect)
            [[ -f $state/ready ]] || fail 'run setup first'
            start
            collect ;;
        ssh|codex)
            [[ -f $state/ready ]] || fail 'run setup first'
            start
            if [[ $action == codex ]]; then launch_agent; fi
            # Interactive sessions must not hold the lifecycle lock.
            flock -u 9
            if [[ $action == ssh ]]; then ssh_guest -t dev;
            else ssh_guest -t dev 'tmux attach-session -t codex'; fi
            ;;
    esac
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
