%{!?servicectl_version:%global servicectl_version 0.2.0}
%{!?stack_release:%global stack_release 1}
%global skalibs_version 2.15.0.0
%global execline_version 2.9.9.1
%global s6_version 2.15.0.0
%global s6rc_version 0.6.1.1
%global dinit_version 0.22.1

Name:           servicectl-stack
Version:        %{servicectl_version}
Release:        %{stack_release}%{?dist}
Summary:        Complete servicectl, Dinit, and s6 service-management stack
License:        MIT AND ISC AND Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause
URL:            https://github.com/NEURAX-FX/servicectl
Source0:        servicectl-%{servicectl_version}.tar.gz
Source1:        skalibs-%{skalibs_version}.tar.gz
Source2:        execline-%{execline_version}.tar.gz
Source3:        s6-%{s6_version}.tar.gz
Source4:        s6-rc-%{s6rc_version}.tar.gz
Source5:        dinit-%{dinit_version}.tar.xz
Source6:        servicectl.tmpfiles
Source7:        sources.lock

BuildRequires:  bash
BuildRequires:  binutils
BuildRequires:  coreutils
BuildRequires:  findutils
BuildRequires:  gcc
BuildRequires:  gcc-c++
BuildRequires:  gzip
BuildRequires:  golang >= 1.22
BuildRequires:  grep
BuildRequires:  libcap-devel
BuildRequires:  m4
BuildRequires:  make
BuildRequires:  pkgconf-pkg-config
BuildRequires:  sed
BuildRequires:  systemd-rpm-macros
BuildRequires:  tar
BuildRequires:  xz

Requires:       servicectl = %{servicectl_version}-%{release}
Requires:       servicectl-dinit = %{dinit_version}-%{release}
Requires:       servicectl-s6 = %{s6_version}-%{release}
%description
Meta package for a self-contained servicectl stack built from one source RPM.
It installs servicectl, Dinit, execline, s6, and s6-rc without replacing PID 1
or starting a supervision tree during package installation.

%package -n servicectl
Version:        %{servicectl_version}
Summary:        systemctl-like control surface for Dinit and s6
License:        MIT AND BSD-2-Clause AND BSD-3-Clause
Requires:       bash
Requires:       coreutils
Requires:       dbus-common
Requires:       dbus-daemon
Requires:       procps-ng
Requires:       servicectl-dinit = %{dinit_version}-%{release}
Requires:       servicectl-s6 = %{s6_version}-%{release}
Requires:       systemd

%description -n servicectl
servicectl translates systemd-style unit definitions into Dinit services and
uses s6-rc as its persistent enablement and orchestration layer.

%package -n servicectl-dinit
Version:        %{dinit_version}
Summary:        Dinit service manager for the servicectl stack
License:        Apache-2.0
Requires:       libcap%{?_isa}
Provides:       dinit = %{dinit_version}-%{release}

%description -n servicectl-dinit
Dinit executables and manual pages configured to coexist with systemd. The
package deliberately omits shutdown aliases and does not replace PID 1.

%package -n servicectl-s6
Version:        %{s6_version}
Summary:        execline, s6, and s6-rc commands for servicectl
License:        ISC
Requires:       servicectl-execline-libs = %{execline_version}-%{release}
Requires:       servicectl-s6-libs = %{s6_version}-%{release}
Requires:       servicectl-s6-rc-libs = %{s6rc_version}-%{release}
Requires:       servicectl-skalibs-libs = %{skalibs_version}-%{release}
Provides:       execline = %{execline_version}-%{release}
Provides:       s6 = %{s6_version}-%{release}
Provides:       s6-rc = %{s6rc_version}-%{release}

%description -n servicectl-s6
The execline, s6, and s6-rc command sets used by servicectl. Installation
creates only the persistent source database root; it does not start s6-svscan
or create a live s6-rc state directory.

%package -n servicectl-skalibs-libs
Version:        %{skalibs_version}
Summary:        skalibs runtime library for the servicectl stack
License:        ISC

%description -n servicectl-skalibs-libs
Runtime shared library from skalibs.

%package -n servicectl-execline-libs
Version:        %{execline_version}
Summary:        execline runtime library for the servicectl stack
License:        ISC
Requires:       servicectl-skalibs-libs = %{skalibs_version}-%{release}

%description -n servicectl-execline-libs
Runtime shared library used by execline and s6-rc.

%package -n servicectl-s6-libs
Version:        %{s6_version}
Summary:        s6 runtime libraries for the servicectl stack
License:        ISC
Requires:       servicectl-execline-libs = %{execline_version}-%{release}
Requires:       servicectl-skalibs-libs = %{skalibs_version}-%{release}

%description -n servicectl-s6-libs
Runtime shared libraries used by s6 and s6-rc.

%package -n servicectl-s6-rc-libs
Version:        %{s6rc_version}
Summary:        s6-rc runtime libraries for the servicectl stack
License:        ISC
Requires:       servicectl-s6-libs = %{s6_version}-%{release}
Requires:       servicectl-skalibs-libs = %{skalibs_version}-%{release}

%description -n servicectl-s6-rc-libs
Runtime shared libraries used by s6-rc.

%package devel
Summary:        Development files for the bundled Skarnet libraries
License:        ISC
Requires:       servicectl-execline-libs = %{execline_version}-%{release}
Requires:       servicectl-s6-libs = %{s6_version}-%{release}
Requires:       servicectl-s6-rc-libs = %{s6rc_version}-%{release}
Requires:       servicectl-skalibs-libs = %{skalibs_version}-%{release}

%description devel
Headers, static archives, linker symlinks, pkg-config metadata, and skalibs
sysdeps for software built against the bundled Skarnet libraries.

%prep
printf '%s  %s\n' 7fde96e8afb4191593a15328883e9c7726c96891cf071222146821e8c87f8007 %{SOURCE1} | sha256sum -c -
printf '%s  %s\n' be63533297a93c36fd267195117b4e668687a526f834517a8db47d85b6c7ec6a %{SOURCE2} | sha256sum -c -
printf '%s  %s\n' 27dff73d626285540133e075e75887087f5117fd51de59503ef7d29e96f69e4c %{SOURCE3} | sha256sum -c -
printf '%s  %s\n' b54f226a35be1ee56a228bf1a4c39437f072bc64e69dbf356e733e606a86402d %{SOURCE4} | sha256sum -c -
printf '%s  %s\n' 959b35c171452ecfbc09379b516517dadd675350eebc57ca54aebedda05d9adf %{SOURCE5} | sha256sum -c -
%setup -q -n servicectl-%{servicectl_version}
cp vendor/github.com/godbus/dbus/v5/LICENSE LICENSE.godbus
cp vendor/golang.org/x/sys/LICENSE LICENSE.xsys
mkdir -p _deps
tar -xzf %{SOURCE1} -C _deps
tar -xzf %{SOURCE2} -C _deps
tar -xzf %{SOURCE3} -C _deps
tar -xzf %{SOURCE4} -C _deps
tar -xf %{SOURCE5} -C _deps
cp _deps/skalibs-%{skalibs_version}/COPYING LICENSE.skalibs
cp _deps/execline-%{execline_version}/COPYING LICENSE.execline
cp _deps/s6-%{s6_version}/COPYING LICENSE.s6
cp _deps/s6-rc-%{s6rc_version}/COPYING LICENSE.s6-rc

%build
%set_build_flags
stage="$PWD/.stage"
mkdir -p "$stage"

skalibs="_deps/skalibs-%{skalibs_version}"
execline="_deps/execline-%{execline_version}"
s6="_deps/s6-%{s6_version}"
s6rc="_deps/s6-rc-%{s6rc_version}"
dinit="_deps/dinit-%{dinit_version}"

"$skalibs/configure" \
  --prefix=%{_prefix} \
  --dynlibdir=%{_libdir} \
  --libdir=%{_libdir} \
  --includedir=%{_includedir} \
  --sysconfdir=%{_sysconfdir} \
  --pkgconfdir=%{_libdir}/pkgconfig \
  --sysdepdir=%{_libdir}/skalibs/sysdeps \
  --enable-shared \
  --disable-rpath \
  --enable-pkgconfig
%make_build -C "$skalibs"
make -C "$skalibs" DESTDIR="$stage" install

common_dependency_args="--with-sysdeps=$stage%{_libdir}/skalibs/sysdeps --with-include=$stage%{_includedir} --with-lib=$stage%{_libdir} --with-dynlib=$stage%{_libdir}"

"$execline/configure" \
  --prefix=%{_prefix} \
  --exec-prefix=%{_prefix} \
  --dynlibdir=%{_libdir} \
  --bindir=%{_bindir} \
  --libexecdir=%{_libexecdir}/execline \
  --libdir=%{_libdir} \
  --includedir=%{_includedir} \
  --sysconfdir=%{_sysconfdir} \
  --pkgconfdir=%{_libdir}/pkgconfig \
  --shebangdir=%{_bindir} \
  $common_dependency_args \
  --enable-shared \
  --disable-allstatic \
  --disable-rpath \
  --enable-pkgconfig
%make_build -C "$execline"
make -C "$execline" DESTDIR="$stage" install

"$s6/configure" \
  --prefix=%{_prefix} \
  --exec-prefix=%{_prefix} \
  --dynlibdir=%{_libdir} \
  --bindir=%{_bindir} \
  --libexecdir=%{_libexecdir}/s6 \
  --libdir=%{_libdir} \
  --includedir=%{_includedir} \
  --sysconfdir=%{_sysconfdir} \
  --pkgconfdir=%{_libdir}/pkgconfig \
  $common_dependency_args \
  --enable-shared \
  --disable-allstatic \
  --disable-rpath \
  --enable-pkgconfig
%make_build -C "$s6"
make -C "$s6" DESTDIR="$stage" install

"$s6rc/configure" \
  --prefix=%{_prefix} \
  --exec-prefix=%{_prefix} \
  --dynlibdir=%{_libdir} \
  --bindir=%{_bindir} \
  --libexecdir=%{_libexecdir}/s6-rc \
  --libdir=%{_libdir} \
  --includedir=%{_includedir} \
  --sysconfdir=%{_sysconfdir} \
  --pkgconfdir=%{_libdir}/pkgconfig \
  --bootdb=%{_sysconfdir}/s6-rc/compiled/current \
  --livedir=%{_rundir}/s6/state \
  --repodir=%{_sharedstatedir}/s6-rc/repository \
  $common_dependency_args \
  --enable-shared \
  --disable-allstatic \
  --disable-rpath \
  --enable-pkgconfig
%make_build -C "$s6rc"
make -C "$s6rc" DESTDIR="$stage" install

"$dinit/configure" \
  --prefix=%{_prefix} \
  --bindir=%{_bindir} \
  --sbindir=%{_bindir} \
  --mandir=%{_mandir} \
  --syscontrolsocket=%{_rundir}/dinitctl \
  --disable-strip \
  --disable-shutdown \
  CXX="%{__cxx}" \
  CXXFLAGS="%{build_cxxflags} -std=gnu++17" \
  TEST_CXXFLAGS="%{build_cxxflags} -std=gnu++17" \
  CPPFLAGS="%{?build_cppflags}" \
  LDFLAGS="%{build_ldflags}" \
  TEST_LDFLAGS="%{build_ldflags}"
%make_build -C "$dinit"

mkdir -p _build/bin
export GOFLAGS=-mod=vendor
export GOTOOLCHAIN=local
export CGO_ENABLED=0
go build -trimpath -buildvcs=false -ldflags "-X main.version=%{servicectl_version}" -o _build/bin/servicectl .
go build -trimpath -buildvcs=false -o _build/bin/sys-notifyd ./cmd/sys-notifyd
go build -trimpath -buildvcs=false -o _build/bin/sys-cgroupd ./cmd/sys-cgroupd
go build -trimpath -buildvcs=false -o _build/bin/sys-dbusd ./cmd/sys-dbusd
go build -trimpath -buildvcs=false -o _build/bin/sys-logd ./cmd/sys-logd
go build -trimpath -buildvcs=false -o _build/bin/sys-propertyd ./cmd/sys-propertyd
go build -trimpath -buildvcs=false -o _build/bin/sysvisiond ./cmd/sysvisiond
go build -trimpath -buildvcs=false -o _build/bin/sys-orchestrd ./cmd/sys-orchestrd
make -C cmd/sys-dbusd-daemon-helper clean all \
  CPPFLAGS='-DSDBUSD_CONTROL_PATH=\"/run/servicectl/sys-dbusd/control.sock\" -DSDBUSD_DAEMON_PATH=\"/usr/bin/dbus-daemon\"' \
  CFLAGS='%{build_cflags} -std=c17 -Wall -Wextra -Werror' \
  LDFLAGS='%{build_ldflags}'

%install
rm -rf %{buildroot}
stage="$PWD/.stage"
cp -a "$stage/." %{buildroot}/

find "%{buildroot}%{_bindir}" -mindepth 1 -maxdepth 1 \( -type f -o -type l \) -printf '%{_bindir}/%%f\n' | sort > skarnet-binaries.files
if [[ -d "%{buildroot}%{_libexecdir}" ]]; then
  find "%{buildroot}%{_libexecdir}" -type f -printf '/%%P\n' | sed "s#^/#%{_libexecdir}/#" | sort >> skarnet-binaries.files
fi

make -C _deps/dinit-%{dinit_version} DESTDIR=%{buildroot} install
install -Dpm0755 _build/bin/servicectl %{buildroot}%{_bindir}/servicectl
install -Dpm0755 _build/bin/sys-notifyd %{buildroot}%{_bindir}/sys-notifyd
install -Dpm0755 _build/bin/sys-cgroupd %{buildroot}%{_bindir}/sys-cgroupd
install -Dpm0755 _build/bin/sys-dbusd %{buildroot}%{_bindir}/sys-dbusd
install -Dpm0755 _build/bin/sys-logd %{buildroot}%{_bindir}/sys-logd
install -Dpm0755 _build/bin/sys-propertyd %{buildroot}%{_bindir}/sys-propertyd
install -Dpm0755 _build/bin/sysvisiond %{buildroot}%{_bindir}/sysvisiond
install -Dpm0755 _build/bin/sys-orchestrd %{buildroot}%{_bindir}/sys-orchestrd
install -Dpm4750 cmd/sys-dbusd-daemon-helper/sys-dbusd-daemon-helper %{buildroot}%{_libexecdir}/servicectl/sys-dbusd-daemon-helper
install -Dpm0644 packaging/sys-dbusd %{buildroot}%{_sysconfdir}/dinit.d/sys-dbusd
install -Dpm0644 packaging/sys-cgroupd %{buildroot}%{_sysconfdir}/dinit.d/sys-cgroupd
install -Dpm0644 packaging/50-servicectl-activation.conf %{buildroot}%{_prefix}/lib/servicectl/dbus-activation/50-servicectl-activation.conf
install -Dpm0644 usr/lib/servicectl/socket-holders.d/dbus.conf %{buildroot}%{_prefix}/lib/servicectl/socket-holders.d/dbus.conf
install -Dpm0644 %{SOURCE6} %{buildroot}%{_tmpfilesdir}/servicectl.conf
install -d -m0755 %{buildroot}%{_sysconfdir}/dinit.d
install -d -m0755 %{buildroot}%{_sysconfdir}/dbus-1/system-services
install -d -m0755 %{buildroot}/s6/rc
install -d -m0755 %{buildroot}%{_sharedstatedir}/servicectl

%check
export LD_LIBRARY_PATH="$PWD/.stage%{_libdir}"
export GOFLAGS=-mod=vendor
export GOTOOLCHAIN=local
export CGO_ENABLED=0
go test -count=1 ./...
bash scripts/test-install-paths.sh
%make_build -C _deps/dinit-%{dinit_version} check

rm -rf _check-dinit
mkdir -p _check-dinit
printf 'type = process\ncommand = /bin/true\n' > _check-dinit/hello
"$PWD/_deps/dinit-%{dinit_version}/src/dinit-check" --services-dir "$PWD/_check-dinit" hello

rm -rf _check-s6rc
mkdir -p _check-s6rc/source/hello
printf 'oneshot\n' > _check-s6rc/source/hello/type
printf '/bin/true\n' > _check-s6rc/source/hello/up
"$PWD/.stage%{_bindir}/s6-rc-compile" "$PWD/_check-s6rc/compiled" "$PWD/_check-s6rc/source"

for binary in servicectl sys-notifyd sys-cgroupd sys-dbusd sys-logd sys-propertyd sysvisiond sys-orchestrd; do
  test -x "%{buildroot}%{_bindir}/$binary"
done
test ! -e "%{buildroot}%{_bindir}/notify-echod"
test ! -e "%{buildroot}%{_bindir}/notify-sleeper"
test ! -e "%{buildroot}%{_bindir}/test-envd"
test "$(find "%{buildroot}" -xdev -type f -perm /4000 -printf '%%p\n')" = "%{buildroot}%{_libexecdir}/servicectl/sys-dbusd-daemon-helper"
test ! -e "%{buildroot}%{_prefix}/local"
test ! -e "%{buildroot}/bin"
test ! -e "%{buildroot}/lib"
test ! -e "%{buildroot}/run"
! find %{buildroot} -type f -print0 | xargs -0 strings | grep -F "%{buildroot}"

%post -n servicectl
%tmpfiles_create servicectl.conf

%files

%files -n servicectl
%license LICENSE LICENSE.godbus LICENSE.xsys
%doc README.md packaging/SRPM-DESIGN.md
%{_bindir}/servicectl
%{_bindir}/sys-notifyd
%{_bindir}/sys-cgroupd
%{_bindir}/sys-dbusd
%{_bindir}/sys-logd
%{_bindir}/sys-propertyd
%{_bindir}/sysvisiond
%{_bindir}/sys-orchestrd
%{_prefix}/lib/servicectl/
%attr(4750,root,dbus) %{_libexecdir}/servicectl/sys-dbusd-daemon-helper
%{_tmpfilesdir}/servicectl.conf
%config(noreplace) %{_sysconfdir}/dinit.d/sys-dbusd
%config(noreplace) %{_sysconfdir}/dinit.d/sys-cgroupd
%dir %{_sysconfdir}/dbus-1/system-services
%dir %{_sharedstatedir}/servicectl

%files -n servicectl-dinit
%license _deps/dinit-%{dinit_version}/LICENSE
%doc _deps/dinit-%{dinit_version}/README.md
%{_bindir}/dinit
%{_bindir}/dinit-check
%{_bindir}/dinit-monitor
%{_bindir}/dinitctl
%{_mandir}/man5/dinit-service.5*
%{_mandir}/man8/dinit.8*
%{_mandir}/man8/dinit-check.8*
%{_mandir}/man8/dinit-monitor.8*
%{_mandir}/man8/dinitctl.8*
%dir %{_sysconfdir}/dinit.d

%files -n servicectl-s6 -f skarnet-binaries.files
%license LICENSE.execline LICENSE.s6 LICENSE.s6-rc
%dir /s6
%dir /s6/rc

%files -n servicectl-skalibs-libs
%license LICENSE.skalibs
%{_libdir}/libskarnet.so.*

%files -n servicectl-execline-libs
%license LICENSE.execline
%{_libdir}/libexecline.so.*

%files -n servicectl-s6-libs
%license LICENSE.s6
%{_libdir}/libs6.so.*
%{_libdir}/libs6auto.so.*

%files -n servicectl-s6-rc-libs
%license LICENSE.s6-rc
%{_libdir}/libs6rc.so.*
%{_libdir}/libs6rcrepo.so.*

%files devel
%license LICENSE.skalibs LICENSE.execline LICENSE.s6 LICENSE.s6-rc
%{_includedir}/skalibs/
%{_includedir}/execline/
%{_includedir}/s6/
%{_includedir}/s6-rc/
%{_libdir}/libskarnet.a
%{_libdir}/libskarnet.so
%{_libdir}/libexecline.a
%{_libdir}/libexecline.so
%{_libdir}/libs6.a
%{_libdir}/libs6.so
%{_libdir}/libs6auto.a
%{_libdir}/libs6auto.so
%{_libdir}/libs6rc.a
%{_libdir}/libs6rc.so
%{_libdir}/libs6rcrepo.a
%{_libdir}/libs6rcrepo.so
%{_libdir}/pkgconfig/libskarnet.pc
%{_libdir}/pkgconfig/libexecline.pc
%{_libdir}/pkgconfig/libs6.pc
%{_libdir}/pkgconfig/libs6auto.pc
%{_libdir}/pkgconfig/libs6rc.pc
%{_libdir}/pkgconfig/libs6rcrepo.pc
%{_libdir}/skalibs/

%changelog
* Sat Jul 11 2026 servicectl contributors <noreply@example.invalid> - %{servicectl_version}-%{stack_release}
- Add sys-cgroupd cgroup v2 process tracking

* Fri Jul 10 2026 servicectl contributors <noreply@example.invalid> - 0.1.0-1
- Initial self-contained Fedora stack package
