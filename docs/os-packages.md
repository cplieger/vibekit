# OS packages

The tools engine installs OS packages, so there is no separate variable for them.
Add one from **Settings → Tools** the way you add anything else, or by name with
an explicit source:

```json
{ "tools": { "gcc": { "source": "apt:gcc" }, "libc6-dev": { "source": "apt:libc6-dev" } } }
```

Two cases need this. Go work that runs `go test -race` needs a C compiler the
image does not ship. And a runtime the engine installs can link a shared library
the image lacks, so the tool installs and then refuses to start; the tools panel
names the missing library on that runtime's row.

What an `apt:` entry buys over `apt-get install` in the shell is the record: the
entry is on the `/config` volume, so a container recreate reinstalls the package
instead of losing it, and the row reports the installed version. Removing the
entry is a logged no-op rather than an uninstall, because apt packages are
shared.

Plain package names only. A version pin (`pkg=1.2`), `pkg:arch`, `pkg/release`,
a trailing `-` (apt reads that as a removal), a name absent from the package
index, and a pure virtual package such as `awk` (name a concrete provider such
as `mawk`) are each refused with the reason. Pinning an entry holds the installed
version and marks it held in dpkg.

An `apt:` entry can only ever be a literal Debian package name, and its
integrity is Debian's signed archive metadata. See the README's Security section
for how that compares with a `release:` entry.
