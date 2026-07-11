# Fedora SRPM Packaging

The packaging directory builds one offline-rebuildable source RPM containing
servicectl, skalibs, execline, s6, s6-rc, Dinit, and the vendored Go module
sources. The source RPM emits separate binary RPMs plus a `servicectl-stack`
meta package for one-command installation.

The first supported target is Fedora 44 and newer. Installing the RPMs does
not replace PID 1, start Dinit, or start an s6 supervision tree. On upgrades
where a live servicectl S6 graph already exists, the scriptlet migrates
`sys-cgroupd` into that graph and starts it there.

## Prerequisites

Install the SRPM preparation tools:

```bash
sudo dnf install git golang rpm-build curl tar coreutils
```

For a local binary rebuild outside Mock, also install the spec build
dependencies:

```bash
sudo dnf install \
  bash binutils coreutils findutils gcc gcc-c++ gzip golang grep \
  libcap-devel m4 make pkgconf-pkg-config sed systemd-rpm-macros tar xz
```

## Build the SRPM

Populate and verify the third-party source cache:

```bash
./packaging/fetch-sources.sh
```

Create the SRPM from a clean worktree:

```bash
./packaging/build-srpm.sh
```

The resulting file is written to `dist/srpm/` by default. The script rejects a
dirty worktree so package contents cannot accidentally diverge from source
control. For an intentional development snapshot:

```bash
./packaging/build-srpm.sh --allow-dirty
```

Select an output directory and explicit package version when needed:

```bash
./packaging/build-srpm.sh \
  --allow-dirty \
  --version 0.1.0 \
  --release 1 \
  --output-dir /tmp/servicectl-srpm
```

## Offline Build

Once the cache has been populated, source preparation needs no network access:

```bash
./packaging/fetch-sources.sh --offline
./packaging/build-srpm.sh --offline
```

Use `--cache-dir DIR` with both commands to maintain a shared source cache.
Every cached archive is verified against `packaging/sources.lock` before use.

## Rebuild Binary RPMs

The recommended clean build uses Mock:

```bash
sudo dnf install mock
mock -r fedora-44-$(uname -m) --rebuild dist/srpm/servicectl-stack-*.src.rpm
```

A direct local rebuild is also supported when all BuildRequires are installed:

```bash
rpmbuild --rebuild \
  --define '_topdir /tmp/servicectl-rpmbuild' \
  dist/srpm/servicectl-stack-*.src.rpm
```

## Install

Install the runtime RPMs in one transaction so local library dependencies are
resolved together. The leading version digit in each glob excludes debuginfo
and development packages:

```bash
sudo dnf install \
  /path/to/RPMS/*/servicectl-stack-[0-9]*.rpm \
  /path/to/RPMS/*/servicectl-[0-9]*.rpm \
  /path/to/RPMS/*/servicectl-dinit-[0-9]*.rpm \
  /path/to/RPMS/*/servicectl-s6-[0-9]*.rpm \
  /path/to/RPMS/*/servicectl-skalibs-libs-[0-9]*.rpm \
  /path/to/RPMS/*/servicectl-execline-libs-[0-9]*.rpm \
  /path/to/RPMS/*/servicectl-s6-libs-[0-9]*.rpm \
  /path/to/RPMS/*/servicectl-s6-rc-libs-[0-9]*.rpm
```

The `servicectl-stack` meta package requires the main CLI, Dinit, and s6
command packages. Development headers are optional and are not required on a
runtime host.

After installation:

```bash
servicectl version
dinit --version
s6-rc help
```

RPM scriptlets do not start a supervisor. The operator must explicitly start a
Dinit control process and initialize an s6 supervision tree before using live
service operations. If that S6 tree is already live during an install or
upgrade, the `sys-cgroupd` migration is applied to it automatically.

## Inspect and Remove

Inspect an SRPM or binary RPM without installing it:

```bash
rpm -qpl dist/srpm/servicectl-stack-*.src.rpm
rpm -qpR /path/to/servicectl-*.rpm
```

Remove the stack with DNF:

```bash
sudo dnf remove servicectl-stack servicectl servicectl-dinit servicectl-s6
```

RPM removal does not recursively delete mutable service state below
`/var/lib/servicectl` or user-owned configuration directories.

See [SRPM-DESIGN.md](SRPM-DESIGN.md) for package boundaries, filesystem
ownership, source versions, and safety decisions.
