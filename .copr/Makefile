srpm:
	(command -v dnf >/dev/null && dnf -y install git golang curl tar xz rpm-build coreutils) || \
	(command -v zypper >/dev/null && zypper -n install git go curl tar xz rpm-build coreutils)
	packaging/build-srpm.sh --output-dir "$(outdir)" --allow-dirty