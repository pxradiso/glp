package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// writeDesktop produces a .desktop file in ~/.local/share/applications and the
// icon in the hicolor tree. Returns the paths written (icon may be empty).
func writeDesktop(app *App, appdir string) (desktopFile, iconFile string, err error) {
	if err := mustMkdirAll(appsDir()); err != nil {
		return "", "", err
	}

	iconFile, iconName, err := installIcon(app, appdir)
	if err != nil {
		return "", "", err
	}

	execPath := execTarget(app, appdir)
	workdir := ""
	if app.Workdir {
		workdir = filepath.Join(dataDir(), app.Name, "current")
	}
	var content string
	if existing := findDesktopFile(appdir); existing != "" {
		raw, rerr := os.ReadFile(existing)
		if rerr != nil {
			return "", "", rerr
		}
		content = patchDesktop(string(raw), execPath, iconName, desktopName(app), workdir)
	} else {
		content = generateDesktop(app, execPath, iconName, workdir)
	}

	dest := filepath.Join(appsDir(), app.Name+".desktop")
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return "", "", err
	}
	return dest, iconFile, nil
}

func execTarget(app *App, appdir string) string {
	rel, err := execRel(app, appdir)
	if err != nil {
		rel = "AppRun"
	}
	p := filepath.Join(dataDir(), app.Name, "current", rel)
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

func findDesktopFile(appdir string) string {
	for _, dir := range []string{appdir,
		filepath.Join(appdir, "usr", "share", "applications"),
		filepath.Join(appdir, "share", "applications")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".desktop") {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	// Some vendors keep the desktop file inside their own data directory,
	// like Spotify's usr/share/spotify/spotify.desktop.
	for _, pat := range []string{
		filepath.Join(appdir, "usr", "share", "*", "*.desktop"),
		filepath.Join(appdir, "share", "*", "*.desktop"),
	} {
		if matches, _ := filepath.Glob(pat); len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// patchDesktop rewrites the keys that must point into their own tree.
func patchDesktop(content, execPath, iconName, displayName, workdir string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	seenExec, seenIcon, seenManaged, seenPath := false, false, false, false
	for i, line := range lines {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Name":
			// The catalog curates the menu name, so an upstream entry that
			// says "winboat" shows as "WinBoat" without losing its localized
			// Name[xx] siblings.
			if displayName != "" {
				lines[i] = "Name=" + displayName
			}
		case "Exec":
			lines[i] = "Exec=" + rewriteExec(val, execPath)
			seenExec = true
		case "TryExec":
			lines[i] = "TryExec=" + execPath
		case "Icon":
			lines[i] = "Icon=" + iconName
			seenIcon = true
		case "Path":
			if workdir != "" {
				lines[i] = "Path=" + workdir
			}
			seenPath = true
		case "X-GLP-Managed":
			lines[i] = "X-GLP-Managed=true"
			seenManaged = true
		}
	}
	if !seenExec {
		lines = append(lines, "Exec="+execPath)
	}
	if iconName == "" {
		kept := lines[:0]
		for _, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "Icon=") {
				kept = append(kept, line)
			}
		}
		lines = kept
		seenIcon = true
	}
	if !seenIcon {
		lines = append(lines, "Icon="+iconName)
	}
	if workdir != "" && !seenPath {
		lines = append(lines, "Path="+workdir)
	}
	if !seenManaged {
		lines = append(lines, "X-GLP-Managed=true")
	}
	return strings.Join(lines, "\n") + "\n"
}

func rewriteExec(val, execPath string) string {
	fields := strings.Fields(val)
	if len(fields) == 0 {
		return execPath
	}
	return strings.Join(append([]string{execPath}, fields[1:]...), " ")
}

// desktopName is the menu label to show, preferring the curated catalog value.
func desktopName(app *App) string {
	if app.Desktop != nil && app.Desktop.Name != "" {
		return app.Desktop.Name
	}
	return app.Name
}

func generateDesktop(app *App, execPath, iconName, workdir string) string {
	displayName := app.Name
	comment := app.Summary
	cats := "Utility;"
	terminal := false
	if app.Desktop != nil {
		if app.Desktop.Name != "" {
			displayName = app.Desktop.Name
		}
		if app.Desktop.Comment != "" {
			comment = app.Desktop.Comment
		}
		if len(app.Desktop.Categories) > 0 {
			cats = strings.Join(app.Desktop.Categories, ";") + ";"
		}
		terminal = app.Desktop.Terminal
	}

	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=" + displayName + "\n")
	if comment != "" {
		b.WriteString("Comment=" + comment + "\n")
	}
	b.WriteString("Exec=" + execPath + "\n")
	b.WriteString("TryExec=" + execPath + "\n")
	if workdir != "" {
		b.WriteString("Path=" + workdir + "\n")
	}
	if iconName != "" {
		b.WriteString("Icon=" + iconName + "\n")
	}
	fmt.Fprintf(&b, "Terminal=%t\n", terminal)
	b.WriteString("Categories=" + cats + "\n")
	b.WriteString("X-GLP-Managed=true\n")
	return b.String()
}

// installIcon copies the best icon the AppDir ships into the hicolor theme.
func installIcon(app *App, appdir string) (path, name string, err error) {
	name = app.Name
	want := name
	if app.Desktop != nil && app.Desktop.Icon != "" {
		want = filepath.Base(app.Desktop.Icon)
	}

	// A catalog icon wins: it is the art the vendor meant as a logo, and it
	// covers builds that ship no desktop file at all, like game releases.
	if app.Icon != nil {
		if src := iconPath(appdir, app.Icon.Path); src != "" {
			destDir := filepath.Join(iconsDir(), "256x256", "apps")
			if err := mustMkdirAll(destDir); err != nil {
				return "", "", err
			}
			dest := filepath.Join(destDir, name+".png")
			if err := writeIcon(src, dest, app.Icon.Crop); err != nil {
				return "", "", err
			}
			return dest, name, nil
		}
	}

	var candidates []string
	candidates = append(candidates, filepath.Join(appdir, ".DirIcon"))
	for _, pat := range []string{
		filepath.Join(appdir, want+".png"),
		filepath.Join(appdir, want+".svg"),
		filepath.Join(appdir, "usr", "share", "icons", "hicolor", "*", "apps", want+".png"),
		filepath.Join(appdir, "usr", "share", "icons", "hicolor", "*", "apps", want+".svg"),
		filepath.Join(appdir, "usr", "share", "icons", "*", "*", "apps", want+".png"),
		filepath.Join(appdir, "usr", "share", "icons", "*", "*", "apps", want+".svg"),
		filepath.Join(appdir, "share", "icons", "hicolor", "*", "apps", want+".png"),
		filepath.Join(appdir, "share", "icons", "hicolor", "*", "apps", want+".svg"),
		filepath.Join(appdir, "chrome", "icons", "default", "default256.png"),
		filepath.Join(appdir, "chrome", "icons", "default", "default128.png"),
		filepath.Join(appdir, "usr", "share", want, "icons", "*256*.png"),
		filepath.Join(appdir, "usr", "share", want, "icons", "*512*.png"),
		filepath.Join(appdir, "usr", "share", want, "icons", "*.png"),
		filepath.Join(appdir, "resources", "app", "resources", "linux", "*.png"),
		filepath.Join(appdir, "product_logo_256.png"),
		filepath.Join(appdir, "browser", "chrome", "icons", "default", "default256.png"),
		filepath.Join(appdir, "browser", "chrome", "icons", "default", "default128.png"),
		filepath.Join(appdir, "*.png"),
	} {
		matches, _ := filepath.Glob(pat)
		candidates = append(candidates, matches...)
	}

	for _, c := range candidates {
		fi, err := os.Stat(c) // follows symlinks, e.g. .DirIcon
		if err != nil || fi.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(c))
		if filepath.Base(c) == ".DirIcon" {
			ext = sniffIconExt(c)
		}
		if ext != ".png" && ext != ".svg" && ext != ".xpm" {
			continue
		}
		destDir := filepath.Join(iconsDir(), "256x256", "apps")
		if ext == ".svg" {
			destDir = filepath.Join(iconsDir(), "scalable", "apps")
		}
		if err := mustMkdirAll(destDir); err != nil {
			return "", "", err
		}
		dest := filepath.Join(destDir, name+ext)
		if err := copyFile(c, dest, 0o644); err != nil {
			return "", "", err
		}
		return dest, name, nil
	}
	// No icon is not fatal: the app still installs and launches, and the
	// desktop entry simply omits the Icon line so no broken icon is shown.
	return "", "", nil
}

// iconPath resolves a catalog icon path inside the extracted tree. Cleaning an
// absolute copy of the path keeps ".." from reaching outside the app.
func iconPath(appdir, rel string) string {
	if !strings.EqualFold(filepath.Ext(rel), ".png") {
		return ""
	}
	p := filepath.Join(appdir, filepath.Clean("/"+rel))
	if !strings.HasPrefix(p, filepath.Clean(appdir)+string(os.PathSeparator)) {
		return ""
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
}

// writeIcon copies a plain icon, or cuts one frame out of a sprite sheet when
// the catalog gives a crop rectangle.
func writeIcon(src, dest string, crop []int) error {
	if len(crop) != 4 {
		return copyFile(src, dest, 0o644)
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	box := image.Rect(crop[0], crop[1], crop[0]+crop[2], crop[1]+crop[3])
	if crop[2] <= 0 || crop[3] <= 0 || !box.In(img.Bounds()) {
		return fmt.Errorf("%s: crop %v does not fit the image", src, crop)
	}
	frame := image.NewNRGBA(image.Rect(0, 0, crop[2], crop[3]))
	draw.Draw(frame, frame.Bounds(), img, box.Min, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, frame); err != nil {
		return err
	}
	return writeFileAtomic(dest, buf.Bytes())
}

// sniffIconExt reads enough of .DirIcon to know whether it is a PNG or an SVG.
// The file often has no extension, so filepath.Ext cannot tell.
func sniffIconExt(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]
	if len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n" {
		return ".png"
	}
	low := strings.ToLower(string(head))
	if strings.Contains(low, "<svg") || strings.Contains(low, "<?xml") {
		return ".svg"
	}
	return ""
}
