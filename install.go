package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func cmdInstall(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: glp install <app>[@version]")
	}
	name, wantVer := splitRef(args[0])

	idx, err := loadIndexFresh()
	if err != nil {
		return err
	}
	app, err := resolveApp(idx, name)
	if err != nil {
		return err
	}
	if wantVer != "" && compareVersions(app.Version, wantVer) != 0 {
		return fmt.Errorf("catalog offers %s %s (requested %s)", app.Name, app.Version, wantVer)
	}
	if !app.ArchMatches(runtime.GOARCH) {
		return fmt.Errorf("%s has no build for %s (available: %s)",
			app.Name, runtime.GOARCH, strings.Join(app.Archs, ", "))
	}
	if m := readMeta(app.Name); m != nil && compareVersions(m.Version, app.Version) >= 0 {
		if binsIntact(m) {
			fmt.Printf("%s %s is already installed (use `glp upgrade`)\n", app.Name, m.Version)
			return nil
		}
		fmt.Printf("%s %s is installed but its launchers are missing, repairing\n", app.Name, m.Version)
	}

	fmt.Printf("\n%s %s\n", app.Name, app.Version)
	if app.Summary != "" {
		fmt.Printf("  %s\n", truncate(app.Summary, 52))
	}
	fmt.Printf("  size    %s\n", humanBytes(app.Size))
	fmt.Printf("  sha256  %s\n", app.SHA256)
	// The packages the app cannot start without are part of the deal, so they
	// are named here and installed by the same yes.
	deps, err := missingDeps(app)
	if err != nil {
		return err
	}
	if len(deps) > 0 {
		fmt.Printf("  needs   %s (root)\n", strings.Join(deps, ", "))
	}
	fmt.Println()
	if !confirm("Install?") {
		return &cancel{msg: "nothing installed"}
	}
	// A yes typed (or piped) by a human covers the packages named right above.
	// GLP_YES=1 is a blanket yes for the app, never for sudo.
	rootApproved := os.Getenv("GLP_YES_ROOT") == "1" || os.Getenv("GLP_YES") != "1"
	// Catch name clashes before spending bandwidth on the download.
	if err := preflightBins(app); err != nil {
		return err
	}

	// Packages go in first: nothing is worse than downloading a gigabyte to
	// find out the app cannot start. A refusal is only fatal for source
	// builds, which cannot compile without their toolchain.
	depsOK := false
	if len(deps) > 0 {
		err := installDeps(deps, rootApproved)
		var need *rootNeeded
		switch {
		case err == nil:
			depsOK = true
		case errors.As(err, &need):
			if app.Build != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\n%s still missing (root not approved)\n  run: %s\n",
				strings.Join(deps, ", "), strings.Join(need.cmd, " "))
		default:
			if app.Build != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "glp: warning: %v\n", err)
		}
	}
	if app.Build != nil && !depsSatisfied(app) {
		return errors.New("cannot build without those packages")
	}

	const steps = 4
	printStep(1, steps, "downloading "+downloadLabel(app))
	blob, err := ensureBlob(app)
	if err != nil {
		return err
	}

	what := "extracting"
	if app.Build != nil {
		what = "building"
	}
	s2 := startStep(2, steps, what, false)
	versionDir, err := extractApp(app, blob)
	if err != nil {
		s2.Fail(err.Error())
		return err
	}
	s2.Done()

	s3 := startStep(3, steps, "linking "+primaryBin(app), false)
	if err := linkBins(app, versionDir); err != nil {
		s3.Fail(err.Error())
		return err
	}
	s3.Done()

	s4 := startStep(4, steps, "desktop entry and icon", false)
	desktopFile, iconFile, err := writeDesktop(app, versionDir)
	if err != nil {
		s4.Fail(err.Error())
		return err
	}
	s4.Done()

	if err := writeMeta(Meta{
		Name:        app.Name,
		Version:     app.Version,
		URL:         app.URL,
		SHA256:      app.SHA256,
		Bins:        app.Bins,
		DesktopFile: desktopFile,
		IconFile:    iconFile,
		Size:        app.Size,
		InstalledAt: time.Now(),
	}); err != nil {
		return err
	}

	// If the loader cannot find a library the AppDir does not bundle, offer to
	// install the packages that provide it. This is the "app does not start"
	// case that usually means one missing system library.
	if libs, lerr := missingLibs(binFor(app.Name, app.Bins), versionDir); lerr == nil && len(libs) > 0 {
		switch {
		case len(deps) > 0 && !depsOK:
			// The hint above already names the command to run.
		case len(deps) > 0:
			fmt.Fprintf(os.Stderr, "\nstill missing: %s\n  the catalog package did not provide it, try `glp doctor %s`\n",
				strings.Join(libs, ", "), app.Name)
		default:
			fmt.Fprintf(os.Stderr, "\n%d libraries missing, run `glp doctor %s`\n", len(libs), app.Name)
			if err := fixRuntime(app.Name, app.Bins); err != nil {
				fmt.Fprintf(os.Stderr, "glp: warning: %v\n", err)
			}
		}
	}

	fmt.Printf("\nrun: %s\n", primaryBin(app))
	return nil
}

func primaryBin(app *App) string {
	if len(app.Bins) > 0 {
		return binName(app.Bins[0], app.Bins[0])
	}
	return app.Name
}

// extractApp unpacks the AppImage into <dataDir>/<app>/<version> and points the
// `current` symlink at it. No FUSE, no mounting, atomic swap.
func extractApp(app *App, blob string) (string, error) {
	root := filepath.Join(dataDir(), app.Name)
	versionDir := filepath.Join(root, app.Version)
	if err := mustMkdirAll(root); err != nil {
		return "", err
	}

	tmp, err := os.MkdirTemp(root, ".extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	exe := filepath.Join(tmp, "payload")
	if err := copyFile(blob, exe, 0o755); err != nil {
		return "", err
	}

	dest := filepath.Join(tmp, "root")
	if err := mustMkdirAll(dest); err != nil {
		return "", err
	}

	var appdir string
	if app.Build != nil {
		src := filepath.Join(tmp, "src")
		if err := mustMkdirAll(src); err != nil {
			return "", err
		}
		if err := unpack(app, exe, src); err != nil {
			return "", err
		}
		if err := runBuild(app, flattenDir(src), dest, versionDir); err != nil {
			return "", err
		}
		appdir = dest
	} else {
		if err := unpack(app, exe, dest); err != nil {
			return "", err
		}
		appdir = resolveAppDir(dest, 3)
		if appdir == "" {
			// A deb already mirrors the filesystem, with usr/ at the root.
			// Flattening would break every path in it.
			if looksLikeTree(dest) {
				appdir = dest
			} else {
				appdir = flattenDir(dest)
			}
		}
		if appdir == "" || !fileExists(filepath.Join(appdir, "AppRun")) {
			// Some vendors zip a single AppImage, sometimes a few folders
			// deep, so unpack that too.
			if nested := onlyAppImage(dest, 4); nested != "" {
				out := filepath.Join(dest, ".nested-appimage")
				if err := mustMkdirAll(out); err != nil {
					return "", err
				}
				os.Chmod(nested, 0o755)
				if err := unpackAppImage(nested, out); err != nil {
					return "", err
				}
				os.Remove(nested)
				appdir = resolveAppDir(out, 2)
			}
		}
		if appdir == "" {
			appdir = flattenDir(dest)
		}
	}
	if fi, err := os.Stat(appdir); err != nil || !fi.IsDir() {
		return "", errors.New("extraction produced no AppDir")
	}

	os.RemoveAll(versionDir)
	if err := os.Rename(appdir, versionDir); err != nil {
		return "", err
	}
	if err := updateCurrent(root, app.Version); err != nil {
		return "", err
	}
	return versionDir, nil
}

// unpack dispatches on the payload format declared by the catalog.
// runBuild walks the signed catalog build steps. Each step is an argv slice, so
// nothing is ever handed to a shell.
func runBuild(app *App, src, dest, final string) error {
	if app.Build == nil || len(app.Build.Commands) == 0 {
		return errors.New("catalog entry has no build steps")
	}
	env := buildEnv()
	// {final} is where the build will live after the atomic rename, useful for
	// paths the compiler bakes into the binary.
	repl := strings.NewReplacer("{src}", src, "{dest}", dest, "{final}", final)
	for _, step := range app.Build.Commands {
		if len(step) == 0 {
			continue
		}
		argv := make([]string, len(step))
		for i, a := range step {
			argv[i] = repl.Replace(a)
		}
		if strings.TrimSpace(argv[0]) == "" {
			return errors.New("empty command in build steps")
		}
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = src
		c.Env = env
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", argv[0], err)
		}
	}
	return nil
}

// buildEnv makes a Go toolchain installed under ~/.local/go usable even when
// the user's shell does not export it.
func buildEnv() []string {
	env := os.Environ()
	path := os.Getenv("PATH")
	home := os.Getenv("HOME")
	for _, dir := range []string{filepath.Join(home, ".local", "go", "bin"), "/usr/local/go/bin"} {
		if fileExists(filepath.Join(dir, "go")) {
			path = dir + string(os.PathListSeparator) + path
		}
	}
	env = append(env, "PATH="+path)
	return env
}

// onlyAppImage returns the path of a lone .AppImage under dir, or "".
func onlyAppImage(dir string, depth int) string {
	var found []string
	var walk func(string, int)
	walk = func(d string, left int) {
		if left < 0 || len(found) > 1 {
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			if e.IsDir() {
				walk(p, left-1)
				continue
			}
			if strings.HasSuffix(strings.ToLower(e.Name()), ".appimage") {
				found = append(found, p)
			}
		}
	}
	walk(dir, depth)
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

func unpack(app *App, payload, dest string) error {
	switch app.Format {
	case "", "appimage":
		return unpackAppImage(payload, dest)
	case "tar.gz", "tgz":
		return unpackTarGz(payload, dest)
	case "tar.xz", "txz", "tar.bz2", "tbz2", "tar.zst":
		return unpackTarExternal(payload, dest)
	case "zip":
		return unpackZip(payload, dest)
	case "deb":
		return unpackDeb(payload, dest)
	}
	return fmt.Errorf("unsupported archive format %q", app.Format)
}

// unpackZip is for vendors that only ship a zip (Brave, for instance). Zip
// carries the unix mode in the external attributes, so executables stay
// executable after extraction.
func unpackZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		target := filepath.Join(dest, filepath.Clean(f.Name))
		if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes destination: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := mustMkdirAll(target); err != nil {
				return err
			}
			continue
		}
		if err := mustMkdirAll(filepath.Dir(target)); err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		rc.Close()
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

// unpackAppImage tries the AppImage's own extract mode first, then unsquashfs.
func unpackAppImage(exe, dest string) error {
	cmd := exec.Command(exe, "--appimage-extract")
	cmd.Dir = dest
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	firstErr := fmt.Errorf("%v: %s", err, tail(string(out), 3))

	if _, lerr := exec.LookPath("unsquashfs"); lerr == nil {
		u := exec.Command("unsquashfs", "-no-progress", "-d",
			filepath.Join(dest, "squashfs-root"), exe)
		u.Dir = dest
		if out2, uerr := u.CombinedOutput(); uerr == nil {
			return nil
		} else {
			return fmt.Errorf("extraction failed (%v / unsquashfs: %s)", firstErr, tail(string(out2), 3))
		}
	}
	return fmt.Errorf("extraction failed: %w", firstErr)
}

// unpackDeb reads the ar container and extracts its data member, which is a
// plain tarball. Spotify only ships a deb, so this keeps it on the same path
// as every other package.
func unpackDeb(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	var magic [8]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != "!<arch>\n" {
		return errors.New("not a deb package")
	}

	for {
		var hdr [60]byte
		if _, err := io.ReadFull(f, hdr[:]); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if string(hdr[58:60]) != "`\n" {
			return errors.New("corrupt deb member header")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(hdr[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil {
			return fmt.Errorf("corrupt deb member size: %w", err)
		}

		if !strings.HasPrefix(name, "data.tar") {
			// Members are padded to an even offset.
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return err
			}
			continue
		}

		// Keep the tarball next to the extraction dir: a deb can be large and
		// /tmp is not always roomy.
		tmp, err := os.CreateTemp(filepath.Dir(dest), "deb-data-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if _, err := io.CopyN(tmp, f, size); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if strings.HasSuffix(name, ".tar.gz") {
			return unpackTarGz(tmpName, dest)
		}
		return unpackTarExternal(tmpName, dest)
	}
	return errors.New("deb package has no data archive")
}

// unpackTarGz extracts in-process. Every entry is checked to stay inside dest.
func unpackTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, filepath.Clean(hdr.Name))
		if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes destination: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mustMkdirAll(target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := mustMkdirAll(filepath.Dir(target)); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := mustMkdirAll(filepath.Dir(target)); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := mustMkdirAll(filepath.Dir(target)); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Link(filepath.Join(dest, filepath.Clean(hdr.Linkname)), target); err != nil {
				return err
			}
		}
	}
}

// unpackTarExternal handles formats the stdlib lacks (xz, zstd, bzip2).
func unpackTarExternal(archive, dest string) error {
	if _, err := exec.LookPath("tar"); err != nil {
		return fmt.Errorf("this archive needs the `tar` command, which is missing")
	}
	c := exec.Command("tar", "-xf", archive, "-C", dest)
	c.Dir = dest
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("extraction failed: %v: %s", err, tail(string(out), 3))
	}
	return nil
}

// resolveAppDir finds the directory holding AppRun, following a single nested
// directory at each level. Some AppImages ship AppDir/ plus a squashfs-root
// symlink, so one flatten is not enough.
func resolveAppDir(dir string, depth int) string {
	if depth < 0 {
		return ""
	}
	if fileExists(filepath.Join(dir, "AppRun")) {
		return dir
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(dir, e.Name()))
		}
	}
	if len(dirs) == 1 {
		return resolveAppDir(dirs[0], depth-1)
	}
	return ""
}

// looksLikeTree reports whether dir already holds a normal unix layout, which
// means it is the root and must not be flattened away.
func looksLikeTree(dir string) bool {
	for _, sub := range []string{"usr/bin", "usr/share", "usr/lib", "bin", "share/applications"} {
		if fileExists(filepath.Join(dir, sub)) {
			return true
		}
	}
	return false
}

// flattenDir drops a single top-level directory, so both "app.tar.gz" layouts
// (with or without a wrapping folder) end up as a plain AppDir.
func flattenDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return dir
	}
	return filepath.Join(dir, entries[0].Name())
}

func updateCurrent(root, ver string) error {
	link := filepath.Join(root, "current")
	tmp := link + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(ver, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// preflightBins fails early when a launcher name is already taken by something
// glp does not manage, so the user does not download 150 MB to find out.
func preflightBins(app *App) error {
	ownRoot := filepath.Join(dataDir(), app.Name)
	for _, spec := range app.Bins {
		link := filepath.Join(binDir(), binName(spec, spec))
		if _, err := os.Lstat(link); err != nil {
			continue
		}
		if !launcherOwned(link, ownRoot) {
			return fmt.Errorf("%s already exists and is not managed by glp. Move it away or rename it, then retry", link)
		}
	}
	return nil
}

func linkBins(app *App, versionDir string) error {
	if err := mustMkdirAll(binDir()); err != nil {
		return err
	}
	root := filepath.Join(dataDir(), app.Name)
	for _, spec := range app.Bins {
		rel, err := relFor(app, versionDir, spec)
		if err != nil {
			return fmt.Errorf("%s: %w", app.Name, err)
		}
		link := filepath.Join(binDir(), binName(spec, rel))
		// Refuse to hijack a name owned by a different app.
		if !launcherOwned(link, root) && fileExists(link) {
			return fmt.Errorf("%s already belongs to another app, not overwriting", link)
		}
		target := filepath.Join(root, "current", rel)
		// AppRun finds its own directory from $0, and some engines look for
		// their native library in the current directory. A symlink would
		// send the first to ~/.local/bin and the second to wherever the user
		// happened to be, so both get a small wrapper instead.
		if rel == "AppRun" || app.Workdir {
			dir := ""
			if app.Workdir {
				dir = filepath.Join(root, "current")
			}
			if err := writeLauncher(link, target, dir); err != nil {
				return err
			}
			continue
		}
		if err := replaceSymlink(target, link); err != nil {
			return err
		}
	}
	return nil
}

const launcherMark = "# glp launcher"

func writeLauncher(dest, target, workdir string) error {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n" + launcherMark + "\n")
	if workdir != "" {
		b.WriteString("cd " + shellQuote(workdir) + " || exit 1\n")
	}
	b.WriteString("exec " + shellQuote(target) + " \"$@\"\n")
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// launcherOwned reports whether link is a symlink or wrapper that belongs to
// this app's tree.
func launcherOwned(link, root string) bool {
	if tgt, err := os.Readlink(link); err == nil {
		return strings.HasPrefix(filepath.Clean(tgt), root)
	}
	return isOurWrapper(link, root)
}

func isOurWrapper(path, root string) bool {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 512 {
		return false
	}
	s := string(b)
	return strings.Contains(s, launcherMark) && strings.Contains(s, root+string(os.PathSeparator))
}

// removeLauncher deletes a symlink or wrapper, but only when it points into
// root, so a user file with the same name is never touched.
func removeLauncher(link, root string) {
	if tgt, err := os.Readlink(link); err == nil {
		if strings.HasPrefix(filepath.Clean(tgt), root) {
			os.Remove(link)
		}
		return
	}
	if isOurWrapper(link, root) {
		os.Remove(link)
	}
}

// binName is the command the user types. It comes from the catalog entry, so a
// fallback to AppRun still lands as the expected name.
func binName(spec, resolved string) string {
	name, _, _ := strings.Cut(spec, "=")
	if name != "" {
		if base := filepath.Base(name); base != "." && base != string(os.PathSeparator) {
			return base
		}
	}
	return filepath.Base(resolved)
}

// execRel is what the launchers point at. For an AppImage that is AppRun, which
// sets APPDIR and the library paths; calling an inner binary directly usually
// works but breaks on the apps that really need their bundled libraries.
func execRel(app *App, appdir string) (string, error) {
	if len(app.Bins) > 0 {
		return relFor(app, appdir, app.Bins[0])
	}
	if fileExists(filepath.Join(appdir, "AppRun")) {
		return "AppRun", nil
	}
	return "", errors.New("nothing to launch in the AppDir")
}

func relFor(app *App, appdir, spec string) (string, error) {
	// An AppDir always launches through AppRun, even when the vendor shipped
	// it inside a zip instead of as a bare AppImage.
	if fileExists(filepath.Join(appdir, "AppRun")) {
		return "AppRun", nil
	}
	return resolveBin(appdir, spec)
}

func resolveBin(appdir, spec string) (string, error) {
	if strings.TrimSpace(spec) == "" {
		return "", errors.New("empty binary name in catalog")
	}
	// "command=path" lets the catalog pick the command name independently of
	// the file it points at, e.g. telegram=Telegram.
	if _, path, ok := strings.Cut(spec, "="); ok {
		spec = path
	}
	var cands []string
	if strings.Contains(spec, "/") {
		cands = []string{spec}
	} else {
		cands = []string{spec, "usr/bin/" + spec, "bin/" + spec}
	}
	for _, c := range cands {
		if fi, err := os.Stat(filepath.Join(appdir, c)); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	if fileExists(filepath.Join(appdir, "AppRun")) {
		return "AppRun", nil
	}
	return "", fmt.Errorf("binary %q not found in the AppDir", spec)
}

func replaceSymlink(target, link string) error {
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink, refusing to overwrite", link)
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return os.Symlink(target, link)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
