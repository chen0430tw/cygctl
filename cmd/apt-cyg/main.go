package main

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha512"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	Version = "2.0.0"

	// Hollow-package thresholds (mirrors bash version)
	hollowMinBytes  = 1024
	hollowWarnBytes = 65536
)

// detectCygwinRoot returns the Cygwin installation root by inspecting the
// path of the running executable (expected: $root\bin\apt-cyg.exe).
// Falls back to C:\cygwin64 if detection fails.
func detectCygwinRoot() string {
	exe, err := os.Executable()
	if err == nil {
		// $root\bin\apt-cyg.exe → parent of bin\ is root
		root := filepath.Dir(filepath.Dir(exe))
		if _, err := os.Stat(filepath.Join(root, "etc", "setup")); err == nil {
			return root
		}
	}
	return `C:\cygwin64`
}

var (
	CygwinRoot   = detectCygwinRoot()
	CacheDir     = CygwinRoot + `\var\cache\apt-cyg`
	SetupIni     = CacheDir + `\setup.ini`
	InstalledDir = CygwinRoot + `\etc\setup`

	DefaultMirror = "https://mirrors.kernel.org/sourceware/cygwin"
	CurrentMirror = DefaultMirror
	CurrentCache  = CacheDir
	optArch       = "x86_64"
)

// httpGet performs an HTTP GET with a browser-like User-Agent so that mirrors
// whose CDN blocks Go's default "Go-http-client/1.1" UA (e.g. Tsinghua, USTC)
// return 200 instead of 403.
func httpGet(u string) (*http.Response, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) apt-cyg/"+Version)
	return http.DefaultClient.Do(req)
}

// Global options set by command-line flags
var opts struct {
	NoDeps      bool
	NoScripts   bool
	AllowHollow bool
	Yes         bool // --yes / -y: skip interactive confirmation prompts
}

// Essential packages that must not be removed
var essentialPkgs = []string{
	"bash", "coreutils", "grep", "gzip", "tar", "xz", "sed", "gawk",
	"cygwin", "base-files", "base-cygwin",
}

type Package struct {
	Name        string
	Version     string
	Install     string // path relative to mirror root
	Size        int64  // expected size in bytes
	SHA512      string // expected SHA-512 hash (128 hex chars)
	MD5         string // expected MD5 hash (32 hex chars), legacy
	Depends     []string
	Provides    []string
	Category    string
	Description string
}

type InstalledEntry struct {
	Archive  string // e.g., bash-5.2.21-1.tar.xz
	Explicit int    // 1=user-requested, 0=dependency
}

var versionConstraintRe = regexp.MustCompile(` \([^)]*\)`)

// ==================== MAIN ====================

func main() {
	execName := strings.ToLower(filepath.Base(os.Args[0]))
	isAptAlias := execName == "apt.exe" || execName == "apt"

	if len(os.Args) < 2 {
		showHelp(isAptAlias)
		os.Exit(0)
	}

	// First pass: extract global options before/after command
	var cleanArgs []string
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--nodeps":
			opts.NoDeps = true
		case "--noscripts":
			opts.NoScripts = true
		case "--allow-hollow":
			opts.AllowHollow = true
		case "--yes", "-y", "--assume-yes":
			opts.Yes = true
		default:
			cleanArgs = append(cleanArgs, arg)
		}
	}

	if len(cleanArgs) == 0 {
		showHelp(isAptAlias)
		os.Exit(0)
	}

	// Read configuration from setup.rc (mirror, cache)
	readSetupRC()

	// Ensure cache directory exists (CurrentCache may differ from CacheDir after readSetupRC)
	os.MkdirAll(CurrentCache, 0755)
	os.MkdirAll(InstalledDir, 0755)

	command := cleanArgs[0]
	args := cleanArgs[1:]

	if isAptAlias {
		command = mapAptCommand(command)
	}

	switch command {
	case "update":
		cmdUpdate()

	case "install":
		requireArgs(args, "install")
		cmdInstall(args)

	case "reinstall":
		requireArgs(args, "reinstall")
		cmdReinstall(args)

	case "remove", "uninstall", "purge":
		requireArgs(args, command)
		cmdRemove(args)

	case "upgrade":
		cmdUpgrade(args)

	case "search":
		requireArgs(args, "search")
		cmdSearch(args[0])

	case "list":
		cmdList(args)

	case "listall":
		requireArgs(args, "listall")
		cmdListall(args[0])

	case "listfiles":
		requireArgs(args, "listfiles")
		cmdListfiles(args)

	case "show", "info":
		requireArgs(args, command)
		cmdShow(args[0])

	case "depends":
		requireArgs(args, "depends")
		cmdDepends(args[0])

	case "rdepends":
		requireArgs(args, "rdepends")
		cmdRdepends(args[0])

	case "download":
		requireArgs(args, "download")
		cmdDownload(args)

	case "check":
		requireArgs(args, "check")
		cmdCheck(args)

	case "category":
		requireArgs(args, "category")
		cmdCategory(args)

	case "searchall":
		requireArgs(args, "searchall")
		cmdSearchall(args)

	case "mirror":
		cmdMirror(args)

	case "cache":
		cmdCache(args)

	case "autoremove":
		cmdAutoremove()

	case "clean":
		cmdClean()

	case "--help", "-h", "help":
		showHelp(isAptAlias)

	case "--version", "-v":
		name := "apt-cyg"
		if isAptAlias {
			name = "apt"
		}
		fmt.Printf("%s version %s\n", name, Version)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		showHelp(isAptAlias)
		os.Exit(1)
	}
}

func requireArgs(args []string, cmd string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Error: '%s' requires at least one argument\n", cmd)
		os.Exit(1)
	}
}

func mapAptCommand(cmd string) string {
	mapping := map[string]string{
		"get":          "install",
		"autoclean":    "clean",
		"dist-upgrade": "upgrade",
		"full-upgrade": "upgrade",
	}
	if mapped, ok := mapping[cmd]; ok {
		return mapped
	}
	return cmd
}

func showHelp(isAptAlias bool) {
	name := "apt-cyg"
	if isAptAlias {
		name = "apt"
	}
	fmt.Printf(`%s version %s - package manager for Cygwin

Usage: %s [options] <command> [arguments]

Commands:
  update                 Download fresh package list (setup.ini) from mirror
  install <pkg...>       Install package(s) with dependencies
  reinstall <pkg...>     Reinstall package(s), re-downloading archives
  remove <pkg...>        Remove installed package(s)
  upgrade [pkg...]       Upgrade named or all installed packages
  search <pattern>       Search packages by name or description
  list [pattern]         List installed packages (optionally filter)
  listall <pattern>      Search all available packages in setup.ini
  listfiles <pkg...>     List files installed by package(s)
  show <package>         Show detailed package information
  depends <package>      Show dependency tree
  rdepends <package>     Show reverse dependency tree
  download <pkg...>      Download package archives without installing
  check <pkg...>         Inspect installed packages for hollow/stub installs
  category <cat...>      List packages in named category
  searchall <term...>    Search cygwin.com for packages containing a file
  mirror [url]           Set or show current mirror URL
  cache [dir]            Set or show local package cache directory
  autoremove             Remove packages not needed by anything
  clean                  Delete cached package archives

Options:
  --yes, -y              Assume yes for all prompts (non-interactive / script mode)
  --nodeps               Skip dependency resolution
  --noscripts            Skip postinstall scripts
  --allow-hollow         Proceed even if archive appears to be a stub
  --help, -h             Show this help
  --version, -v          Show version

Examples:
  %s update
  %s install vim git python3
  %s search python
  %s list
  %s upgrade
  %s check bash
  %s mirror https://cygwin.mirror.example.com
`, name, Version, name, name, name, name, name, name, name, name)
}

// ==================== SETUP.RC ====================

func setupRCPath() string {
	return filepath.Join(CygwinRoot, "etc", "setup", "setup.rc")
}

// readSetupRC reads mirror and cache settings from Cygwin's setup.rc.
func readSetupRC() {
	data, err := os.ReadFile(setupRCPath())
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		// Keys may optionally start with "<" in some setup.rc variants
		key := strings.TrimSpace(strings.TrimLeft(line, "<"))
		if i+1 >= len(lines) {
			break
		}
		val := strings.TrimSpace(lines[i+1])
		switch key {
		case "last-mirror":
			if val != "" {
				CurrentMirror = strings.TrimRight(val, "/")
			}
		case "last-cache":
			if val != "" {
				// setup.rc may store a Cygwin path (e.g. /var/cache/apt-cyg).
				// Go's os package needs Windows paths on Windows.
				if strings.HasPrefix(val, "/") {
					if wp, err := toWindowsPath(val); err == nil {
						CurrentCache = wp
					}
				} else {
					CurrentCache = val
				}
			}
		}
	}
}

// writeSetupRCKey updates or appends a key-value pair in setup.rc.
func writeSetupRCKey(key, value string) {
	path := setupRCPath()
	data, _ := os.ReadFile(path)

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		k := strings.TrimSpace(strings.TrimLeft(line, "<"))
		if k == key && i+1 < len(lines) {
			lines[i+1] = "\t" + value
			found = true
			break
		}
	}
	if !found {
		// Remove trailing empty line then append
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "", key, "\t"+value, "")
	}

	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

// ==================== UPDATE ====================

func cmdUpdate() {
	fmt.Println("Updating package list...")

	// Back up existing setup.ini
	bakPath := SetupIni + "-save"
	os.Rename(SetupIni, bakPath)

	if tryDownloadSetup("setup.xz") || tryDownloadSetup("setup.bz2") || tryDownloadSetup("setup.ini") {
		os.Remove(bakPath)
		fmt.Println("Updated setup.ini")
		return
	}

	// Restore backup on failure
	fmt.Fprintln(os.Stderr, "Error updating setup.ini, reverting to backup")
	os.Rename(bakPath, SetupIni)
	os.Exit(1)
}

func tryDownloadSetup(filename string) bool {
	u := CurrentMirror + "/" + optArch + "/" + filename
	tmpPath := filepath.Join(CacheDir, filename)

	fmt.Printf("  Trying %s ... ", filename)

	resp, err := httpGet(u)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		fmt.Println("not available")
		return false
	}

	out, err := os.Create(tmpPath)
	if err != nil {
		resp.Body.Close()
		return false
	}
	_, err = io.Copy(out, resp.Body)
	out.Close()
	resp.Body.Close()

	if err != nil {
		os.Remove(tmpPath)
		return false
	}
	fmt.Println("OK")

	ext := filepath.Ext(filename)
	switch ext {
	case ".xz":
		bashExe := filepath.Join(CygwinRoot, "bin", "bash.exe")
		cygTmp := toCygwinPath(tmpPath)
		cygDest := toCygwinPath(SetupIni)
		decompressed := strings.TrimSuffix(tmpPath, ".xz")
		cmd := exec.Command(bashExe, "--login", "-c",
			fmt.Sprintf("xz -d '%s' && mv '%s' '%s'",
				cygTmp, toCygwinPath(decompressed), cygDest))
		if err := cmd.Run(); err != nil {
			os.Remove(tmpPath)
			return false
		}

	case ".bz2":
		// Use Go's bzip2 reader
		f, err := os.Open(tmpPath)
		if err != nil {
			return false
		}
		br := bzip2.NewReader(f)
		out, err := os.Create(SetupIni)
		if err != nil {
			f.Close()
			return false
		}
		_, copyErr := io.Copy(out, br)
		out.Close()
		f.Close()
		os.Remove(tmpPath)
		if copyErr != nil {
			os.Remove(SetupIni)
			return false
		}

	default: // .ini — already the right format
		os.Rename(tmpPath, SetupIni)
	}
	return true
}

// ==================== SEARCH ====================

func cmdSearch(pattern string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Invalid pattern: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Searching downloaded packages...")
	for name, pkg := range packages {
		if re.MatchString(name) || re.MatchString(pkg.Description) {
			status := " "
			if isInstalled(name) {
				status = "i"
			}
			fmt.Printf("[%s] %-30s %s\n", status, name, pkg.Description)
		}
	}
}

// ==================== LIST ====================

func cmdList(args []string) {
	// List installed packages from installed.db (mirrors bash apt-list)
	db := readInstalledDB()

	var filter string
	if len(args) > 0 && args[0] != "--installed" && args[0] != "-i" {
		filter = args[0]
	}

	if len(db) == 0 {
		fmt.Println("No packages installed.")
		return
	}

	// Sort for deterministic output
	names := make([]string, 0, len(db))
	for name := range db {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if filter != "" && !strings.Contains(name, filter) {
			continue
		}
		entry := db[name]
		fmt.Printf("%-30s %s\n", name, entry.Archive)
	}
}

// ==================== LISTALL ====================

func cmdListall(pattern string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Invalid pattern: %v\n", err)
		os.Exit(1)
	}

	var matched []string
	for name, pkg := range packages {
		if re.MatchString(name) || re.MatchString(pkg.Description) {
			matched = append(matched, name)
		}
	}
	sort.Strings(matched)

	for _, name := range matched {
		pkg := packages[name]
		status := " "
		if isInstalled(name) {
			status = "i"
		}
		fmt.Printf("[%s] %-30s %s\n", status, name, pkg.Description)
	}
	fmt.Printf("\n%d package(s) found.\n", len(matched))
}

// ==================== LISTFILES ====================

func cmdListfiles(names []string) {
	for _, name := range names {
		lstFile := filepath.Join(InstalledDir, name+".lst.gz")
		if _, err := os.Stat(lstFile); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s: not installed or no file manifest\n", name)
			continue
		}

		files := readFileList(lstFile)
		if len(files) == 0 {
			fmt.Fprintf(os.Stderr, "%s: empty file manifest\n", name)
			continue
		}

		for _, f := range files {
			fmt.Printf("/%s\n", strings.TrimPrefix(f, "./"))
		}
	}
}

// ==================== SHOW ====================

func cmdShow(name string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	pkg, ok := packages[name]
	if !ok {
		// Try fuzzy match
		var similar []string
		for pname := range packages {
			if strings.Contains(strings.ToLower(pname), strings.ToLower(name)) {
				similar = append(similar, pname)
			}
		}
		if len(similar) > 0 {
			sort.Strings(similar)
			fmt.Fprintf(os.Stderr, "No exact match for %q. Similar packages:\n", name)
			for _, s := range similar {
				fmt.Fprintf(os.Stderr, "  %s\n", s)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Unable to locate package %q\n", name)
		}
		os.Exit(1)
	}

	fmt.Printf("Package:     %s\n", pkg.Name)
	fmt.Printf("Version:     %s\n", pkg.Version)
	fmt.Printf("Category:    %s\n", pkg.Category)
	if pkg.Size > 0 {
		fmt.Printf("Size:        %s\n", humanSize(pkg.Size))
	}
	if len(pkg.Depends) > 0 {
		fmt.Printf("Depends:     %s\n", strings.Join(pkg.Depends, ", "))
	}
	if len(pkg.Provides) > 0 {
		fmt.Printf("Provides:    %s\n", strings.Join(pkg.Provides, ", "))
	}
	fmt.Printf("Description: %s\n", pkg.Description)
	if isInstalled(name) {
		db := readInstalledDB()
		entry := db[name]
		fmt.Printf("Installed:   yes (%s)\n", entry.Archive)
	} else {
		fmt.Printf("Installed:   no\n")
	}
}

// ==================== DEPENDS ====================

func cmdDepends(name string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if _, ok := packages[name]; !ok {
		fmt.Fprintf(os.Stderr, "Package not found: %s\n", name)
		os.Exit(1)
	}

	printDepsTree(name, packages, nil)
}

// printDepsTree prints the forward dependency tree rooted at name.
// path tracks the current chain for cycle detection.
func printDepsTree(name string, packages map[string]Package, path []string) {
	// Cycle guard
	for _, p := range path {
		if p == name {
			return
		}
	}
	newPath := append(append([]string{}, path...), name)

	// Print current node: show full path joined with " > "
	fmt.Printf("%s\n", strings.Join(newPath, " > "))

	pkg, ok := packages[name]
	if !ok {
		return
	}
	for _, depLine := range pkg.Depends {
		for _, dep := range strings.Split(depLine, ",") {
			depName := cleanDepName(dep)
			if depName == "" {
				continue
			}
			printDepsTree(depName, packages, newPath)
		}
	}
}

// ==================== RDEPENDS ====================

func cmdRdepends(name string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if _, ok := packages[name]; !ok {
		fmt.Fprintf(os.Stderr, "Package not found: %s\n", name)
		os.Exit(1)
	}

	// Build reverse dependency map: dep -> []packages that require dep
	rdeps := make(map[string][]string)
	for pkgName, pkg := range packages {
		for _, depLine := range pkg.Depends {
			for _, dep := range strings.Split(depLine, ",") {
				depName := cleanDepName(dep)
				if depName != "" {
					rdeps[depName] = append(rdeps[depName], pkgName)
				}
			}
		}
	}
	// Sort each entry for deterministic output
	for k := range rdeps {
		sort.Strings(rdeps[k])
	}

	printRdepsTree(name, rdeps, nil)
}

// printRdepsTree prints the reverse dependency tree rooted at name.
func printRdepsTree(name string, rdeps map[string][]string, path []string) {
	// Cycle guard
	for _, p := range path {
		if p == name {
			return
		}
	}
	newPath := append(append([]string{}, path...), name)

	// Print current node: show full path joined with " < "
	fmt.Printf("%s\n", strings.Join(newPath, " < "))

	for _, parent := range rdeps[name] {
		printRdepsTree(parent, rdeps, newPath)
	}
}

// ==================== UPGRADE ====================

func cmdUpgrade(names []string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	db := readInstalledDB()

	var toCheck []string
	if len(names) > 0 {
		for _, name := range names {
			if !isInstalled(name) {
				fmt.Printf("%s is not installed; use 'install' instead\n", name)
				continue
			}
			toCheck = append(toCheck, name)
		}
	} else {
		// Upgrade all installed packages
		for name := range db {
			toCheck = append(toCheck, name)
		}
		sort.Strings(toCheck)
	}

	if len(toCheck) == 0 {
		fmt.Println("No packages to upgrade.")
		return
	}

	upgraded := 0
	for _, name := range toCheck {
		pkg, ok := packages[name]
		if !ok {
			fmt.Printf("Warning: %s not found in repository, skipping.\n", name)
			continue
		}

		currentBn := ""
		if entry, inDB := db[name]; inDB {
			currentBn = entry.Archive
		}
		availableBn := filepath.Base(pkg.Install)

		if currentBn == availableBn {
			fmt.Printf("%s is up to date (%s)\n", name, currentBn)
			continue
		}

		if currentBn != "" {
			fmt.Printf("Upgrading %s: %s -> %s\n", name, currentBn, availableBn)
		} else {
			fmt.Printf("Reinstalling %s: %s\n", name, availableBn)
		}
		installPackage(name, pkg, 1, true)
		upgraded++
	}

	if upgraded == 0 {
		fmt.Println("All packages are up to date.")
	} else {
		if !opts.NoScripts {
			runAllPostinstall()
		}
		fmt.Printf("Upgraded %d package(s).\n", upgraded)
	}
}

// ==================== DOWNLOAD ====================

func cmdDownload(names []string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	for _, name := range names {
		pkg, ok := packages[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "Package not found: %s\n", name)
			continue
		}

		cacheFile := filepath.Join(CurrentCache, filepath.Base(pkg.Install))

		if _, err := os.Stat(cacheFile); err == nil {
			fmt.Printf("%s already downloaded: %s\n", name, cacheFile)
			continue
		}

		u := CurrentMirror + "/" + pkg.Install
		fmt.Printf("Get: %s/%s [%s]\n", CurrentMirror, pkg.Install, humanSize(pkg.Size))

		if err := downloadWithProgress(u, cacheFile, pkg.Size); err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to download %s: %v\n", name, err)
			continue
		}

		if err := verifyHash(name, cacheFile, pkg.SHA512, pkg.MD5); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Remove(cacheFile)
			continue
		}

		fmt.Printf("%s downloaded to %s\n", name, cacheFile)
	}
}

// ==================== CHECK ====================

// cmdCheck inspects installed packages for hollow/stub installs.
// Three checks: (1) cached archive integrity, (2) PE-header validity, (3) file presence.
func cmdCheck(names []string) {
	packages, _ := parseSetupIni()
	db := readInstalledDB()
	totalErrors := 0

	for _, name := range names {
		fmt.Printf("--- Checking %s ---\n", name)

		if !isInstalled(name) {
			fmt.Fprintf(os.Stderr, "  %s is not installed\n", name)
			totalErrors++
			continue
		}

		pkgErrors := 0
		entry, hasEntry := db[name]

		// Check 1: cached archive size and hash
		if hasEntry {
			archivePath := filepath.Join(CurrentCache, entry.Archive)
			if _, err := os.Stat(archivePath); err == nil {
				if err := checkHollow(name, archivePath); err != nil {
					fmt.Fprintf(os.Stderr, "  archive: %v\n", err)
					pkgErrors++
				} else {
					// Re-verify hash against current setup.ini
					if pkg, ok := packages[name]; ok {
						if err := verifyHash(name, archivePath, pkg.SHA512, pkg.MD5); err != nil {
							fmt.Fprintln(os.Stderr, " ", err)
							pkgErrors++
						} else {
							fmt.Printf("  hash: OK\n")
						}
					}
				}
			} else {
				fmt.Printf("  archive: not in cache (%s) — skipping archive/hash check\n", entry.Archive)
			}
		}

		// Check 2: PE-header validation of installed DLLs/EXEs
		if bad, total := checkPEBins(name); bad > 0 {
			fmt.Fprintf(os.Stderr, "  binaries: %d/%d PE files failed MZ-header check\n", bad, total)
			pkgErrors++
		} else if total > 0 {
			fmt.Printf("  binaries: %d PE file(s) OK\n", total)
		}

		// Check 3: presence of every recorded file on disk
		lstFile := filepath.Join(InstalledDir, name+".lst.gz")
		if _, err := os.Stat(lstFile); err == nil {
			files := readFileList(lstFile)
			missing, total := 0, 0
			for _, f := range files {
				if strings.HasSuffix(f, "/") {
					continue
				}
				total++
				fullPath := "/" + strings.TrimPrefix(f, "./")
				if _, err := os.Stat(fullPath); os.IsNotExist(err) {
					fmt.Fprintf(os.Stderr, "  MISSING: /%s\n", strings.TrimPrefix(f, "./"))
					missing++
				}
			}
			if missing > 0 {
				fmt.Fprintf(os.Stderr, "  files: %d/%d missing from filesystem\n", missing, total)
				pkgErrors++
			} else if total > 0 {
				fmt.Printf("  files: %d/%d present\n", total, total)
			}
		} else {
			fmt.Fprintf(os.Stderr, "  files: no manifest found (/etc/setup/%s.lst.gz)\n", name)
		}

		totalErrors += pkgErrors
		if pkgErrors == 0 {
			fmt.Printf("  %s: OK\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "  %s: %d issue(s) found\n", name, pkgErrors)
		}
		fmt.Println()
	}

	if totalErrors > 0 {
		fmt.Fprintf(os.Stderr, "apt-check: %d issue(s) found across %d package(s).\n", totalErrors, len(names))
		os.Exit(1)
	}
	fmt.Printf("apt-check: all %d package(s) OK.\n", len(names))
}

// ==================== CATEGORY ====================

func cmdCategory(cats []string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	for _, cat := range cats {
		fmt.Printf("Packages in category '%s':\n", cat)

		var matched []string
		for name, pkg := range packages {
			for _, c := range strings.Fields(pkg.Category) {
				if strings.EqualFold(c, cat) {
					matched = append(matched, name)
					break
				}
			}
		}
		sort.Strings(matched)

		for _, name := range matched {
			pkg := packages[name]
			status := " "
			if isInstalled(name) {
				status = "i"
			}
			fmt.Printf("  [%s] %-30s %s\n", status, name, pkg.Description)
		}
		if len(matched) == 0 {
			fmt.Printf("  No packages found in category '%s'\n", cat)
		}
	}
}

// ==================== SEARCHALL ====================

// cmdSearchall queries cygwin.com to find which package provides a given file.
func cmdSearchall(terms []string) {
	digitRe := regexp.MustCompile(`-\d`)

	for _, term := range terms {
		fmt.Printf("Searching cygwin.com for '%s'...\n", term)

		u := fmt.Sprintf("https://cygwin.com/cgi-bin2/package-grep.cgi?text=1&arch=%s&grep=%s",
			optArch, url.QueryEscape(term))

		resp, err := httpGet(u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}

		seen := make(map[string]bool)
		scanner := bufio.NewScanner(resp.Body)
		firstLine := true
		for scanner.Scan() {
			line := scanner.Text()
			if firstLine {
				firstLine = false
				continue // skip header line
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Skip debuginfo and 32-bit packages
			if strings.Contains(line, "-debuginfo-") || strings.HasPrefix(line, "cygwin32-") {
				continue
			}
			// Extract package name from "pkgname-version\tfile"
			// Field separator is "-[digit]", mirroring the bash awk
			if tab := strings.IndexByte(line, '\t'); tab >= 0 {
				line = line[:tab]
			}
			pkgName := line
			if loc := digitRe.FindStringIndex(pkgName); loc != nil {
				pkgName = pkgName[:loc[0]]
			}
			if pkgName != "" && !seen[pkgName] {
				seen[pkgName] = true
				fmt.Println(pkgName)
			}
		}
		resp.Body.Close()
	}
}

// ==================== INSTALL ====================

func cmdInstall(names []string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	toInstall := resolveInstallList(names, packages)

	if len(toInstall) == 0 {
		fmt.Println("All packages already installed.")
		return
	}

	fmt.Printf("Will install %d package(s): %s\n", len(toInstall), strings.Join(toInstall, " "))

	for _, name := range toInstall {
		installPackage(name, packages[name], 1, false)
	}

	if !opts.NoScripts {
		runAllPostinstall()
	}
}

func cmdReinstall(names []string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	for _, name := range names {
		pkg, ok := packages[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: Package not found: %s\n", name)
			continue
		}
		// Force redownload by removing cached archive
		cacheFile := filepath.Join(CurrentCache, filepath.Base(pkg.Install))
		os.Remove(cacheFile)
		installPackage(name, pkg, 1, true)
	}

	if !opts.NoScripts {
		runAllPostinstall()
	}
}

// resolveInstallList returns the ordered list of packages to install,
// excluding already-installed ones.
func resolveInstallList(names []string, packages map[string]Package) []string {
	visiting := make(map[string]bool) // current DFS path — cycle guard
	done := make(map[string]bool)     // fully resolved — skip re-processing
	var ordered []string

	for _, name := range names {
		deps := resolveDependencies(name, packages, visiting, done)
		ordered = append(ordered, deps...)
	}

	// Deduplicate and filter already installed
	seen := make(map[string]bool)
	var unique []string
	for _, name := range ordered {
		if !seen[name] && !isInstalled(name) {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	return unique
}

func resolveDependencies(name string, packages map[string]Package, visiting, done map[string]bool) []string {
	// Resolve virtual package names (provides:)
	name = resolveProvides(name, packages)

	// Already fully resolved in a previous branch — return without re-processing.
	if done[name] {
		return []string{name}
	}

	// Cycle guard: package is already on the current DFS stack — skip silently.
	if visiting[name] {
		return nil
	}

	pkg, ok := packages[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "Warning: Package not found: %s\n", name)
		return nil
	}

	visiting[name] = true
	defer func() {
		delete(visiting, name)
		done[name] = true
	}()

	result := []string{name}

	if opts.NoDeps {
		return result
	}

	for _, depLine := range pkg.Depends {
		for _, dep := range strings.Split(depLine, ",") {
			depName := cleanDepName(dep)
			if depName == "" {
				continue
			}
			subDeps := resolveDependencies(depName, packages, visiting, done)
			result = append(result, subDeps...)
		}
	}

	return result
}

// resolveProvides returns the real package name if `name` is a virtual package
// provided by another package.
func resolveProvides(name string, packages map[string]Package) string {
	if _, ok := packages[name]; ok {
		return name
	}
	for pkgName, pkg := range packages {
		for _, provided := range pkg.Provides {
			if strings.EqualFold(strings.TrimSpace(provided), name) {
				fmt.Printf("  (virtual %s provided by %s)\n", name, pkgName)
				return pkgName
			}
		}
	}
	return name
}

// installPackage downloads (if needed), verifies, extracts, and records a package.
// explicit: 1=user-requested, 0=dependency. force: reinstall even if recorded.
func installPackage(name string, pkg Package, explicit int, force bool) {
	if !force && isInstalled(name) {
		fmt.Printf("Package %s is already installed, skipping\n", name)
		return
	}

	if pkg.Install == "" {
		fmt.Fprintf(os.Stderr, "Error: No install path for %s (obsolete package?)\n", name)
		return
	}

	fmt.Printf("\nInstalling %s (%s)...\n", name, pkg.Version)

	cacheFile := filepath.Join(CurrentCache, filepath.Base(pkg.Install))

	// Download if not cached or if force-reinstall
	needDownload := force
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		needDownload = true
	}

	if needDownload {
		u := CurrentMirror + "/" + pkg.Install
		fmt.Printf("Get: %s/%s [%s]\n", CurrentMirror, pkg.Install, humanSize(pkg.Size))

		if err := downloadWithProgress(u, cacheFile, pkg.Size); err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to download %s: %v\n", name, err)
			os.Exit(1)
		}

		// Verify hash
		if err := verifyHash(name, cacheFile, pkg.SHA512, pkg.MD5); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Remove(cacheFile)
			os.Exit(1)
		}
	} else {
		fmt.Printf("  Using cached: %s\n", filepath.Base(cacheFile))
	}

	// Hollow-package check (pre-extraction)
	if !opts.AllowHollow {
		if err := checkHollow(name, cacheFile); err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "Use --allow-hollow to override.")
			os.Exit(1)
		}
	}

	// Save file listing and extract
	fmt.Println("Unpacking...")
	if err := extractPackage(cacheFile, "/", name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to extract %s: %v\n", name, err)
		rollbackInstall(name)
		os.Exit(1)
	}

	// PE-header check (post-extraction)
	if !opts.AllowHollow {
		if bad, _ := checkPEBins(name); bad > 0 {
			fmt.Fprintf(os.Stderr, "Error: %s contains %d ghost binary/binaries (hollow install). Rolling back.\n", name, bad)
			rollbackInstall(name)
			os.Exit(1)
		}
	}

	// Update installed.db
	recordInstall(name, filepath.Base(cacheFile), explicit)

	fmt.Printf("%s installed.\n", name)
}

func extractPackage(archivePath, dst, pkgName string) error {
	bashExe := filepath.Join(CygwinRoot, "bin", "bash.exe")
	cygSrc := toCygwinPath(archivePath)

	// Save file manifest: tar tf $archive | gzip > /etc/setup/$pkg.lst.gz
	lstFile := toCygwinPath(filepath.Join(InstalledDir, pkgName+".lst.gz"))
	listCmd := fmt.Sprintf("tar -tf '%s' | gzip > '%s'", cygSrc, lstFile)
	exec.Command(bashExe, "--login", "-c", listCmd).Run() // best-effort

	// Extract; dst is a Cygwin path (e.g. "/") so tar places files correctly.
	extractCmd := fmt.Sprintf("tar -xf '%s' -C '%s'", cygSrc, dst)
	cmd := exec.Command(bashExe, "--login", "-c", extractCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// rollbackInstall removes extracted files using the manifest if extraction failed.
func rollbackInstall(name string) {
	lstFile := filepath.Join(InstalledDir, name+".lst.gz")
	if _, err := os.Stat(lstFile); err != nil {
		return
	}
	fmt.Printf("Rolling back %s installation...\n", name)
	files := readFileList(lstFile)
	for _, f := range files {
		if strings.HasSuffix(f, "/") {
			continue
		}
		os.Remove("/" + strings.TrimPrefix(f, "./"))
	}
	os.Remove(lstFile)
}

// ==================== REMOVE ====================

func cmdRemove(names []string) {
	for _, name := range names {
		if !isInstalled(name) {
			fmt.Printf("Package %s is not installed, skipping\n", name)
			continue
		}

		// Protect essential packages
		for _, ess := range essentialPkgs {
			if name == ess {
				fmt.Fprintf(os.Stderr, "Error: Cannot remove essential package %s\n", name)
				os.Exit(1)
			}
		}

		fmt.Printf("Removing %s...\n", name)

		// Run pre-remove script
		runPreremove(name)

		// Read file list
		lstFile := filepath.Join(InstalledDir, name+".lst.gz")
		files := readFileList(lstFile)

		// Remove regular files
		for _, f := range files {
			if strings.HasSuffix(f, "/") {
				continue
			}
			fullPath := "/" + strings.TrimPrefix(f, "./")
			os.Remove(fullPath)
		}

		// Remove directories in reverse order (deepest first)
		var dirs []string
		for _, f := range files {
			clean := strings.TrimRight(strings.TrimPrefix(f, "./"), "/")
			if strings.HasSuffix(f, "/") && clean != "" && clean != "." {
				dirs = append(dirs, clean)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		for _, d := range dirs {
			os.Remove("/" + d) // silently fails if not empty
		}

		// Remove postinstall done marker
		os.Remove("/etc/postinstall/" + name + ".sh.done")

		// Remove records
		os.Remove(lstFile)
		removeFromInstalledDB(name)

		fmt.Printf("Package %s removed.\n", name)
	}
}

// ==================== AUTOREMOVE ====================

func cmdAutoremove() {
	fmt.Println("Finding unused dependencies...")

	db := readInstalledDB()
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Build set of packages needed as dependencies
	needed := make(map[string]bool)
	for name := range db {
		if pkg, ok := packages[name]; ok {
			for _, depLine := range pkg.Depends {
				for _, dep := range strings.Split(depLine, ",") {
					depName := cleanDepName(dep)
					if depName != "" {
						needed[depName] = true
					}
				}
			}
		}
	}

	// Find orphans: installed but not needed as dep, and installed as dependency (explicit=0)
	var orphans []string
	for name, entry := range db {
		if needed[name] {
			continue
		}
		if entry.Explicit == 1 {
			continue // explicitly installed by user
		}
		// Skip essential packages
		isEss := false
		for _, ess := range essentialPkgs {
			if name == ess {
				isEss = true
				break
			}
		}
		if isEss {
			continue
		}
		orphans = append(orphans, name)
	}

	if len(orphans) == 0 {
		fmt.Println("No orphaned packages found.")
		return
	}

	sort.Strings(orphans)
	fmt.Printf("The following %d package(s) are no longer needed:\n", len(orphans))
	fmt.Printf("  %s\n", strings.Join(orphans, " "))

	if !opts.Yes {
		fmt.Print("Remove them? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("Aborted.")
			return
		}
	}

	cmdRemove(orphans)
}

// ==================== CLEAN ====================

func cmdClean() {
	fmt.Println("Cleaning package cache...")

	files, err := os.ReadDir(CurrentCache)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	removed := 0
	var totalSize int64
	for _, f := range files {
		if f.Name() == "setup.ini" || f.Name() == "setup.ini-save" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name()))
		if ext != ".xz" && ext != ".bz2" && ext != ".gz" && ext != ".zst" {
			continue // only remove package archives
		}
		info, err := f.Info()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to stat %s: %v\n", f.Name(), err)
			continue
		}
		totalSize += info.Size()
		if err := os.Remove(filepath.Join(CurrentCache, f.Name())); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to remove %s: %v\n", f.Name(), err)
		} else {
			removed++
		}
	}

	fmt.Printf("Removed %d file(s), freed %s\n", removed, humanSize(totalSize))
}

// ==================== MIRROR / CACHE ====================

func cmdMirror(args []string) {
	if len(args) == 0 {
		fmt.Println(CurrentMirror)
		return
	}
	newMirror := strings.TrimRight(args[0], "/")
	writeSetupRCKey("last-mirror", newMirror)
	CurrentMirror = newMirror
	fmt.Printf("Mirror set to %s\n", CurrentMirror)
}

func cmdCache(args []string) {
	if len(args) == 0 {
		fmt.Println(CurrentCache)
		return
	}
	newCache := args[0]
	// Try to convert to Windows path if it looks like a Cygwin path
	if strings.HasPrefix(newCache, "/") {
		if wp, err := toWindowsPath(newCache); err == nil {
			newCache = wp
		}
	}
	writeSetupRCKey("last-cache", newCache)
	CurrentCache = newCache
	fmt.Printf("Cache set to %s\n", CurrentCache)
}

// ==================== INSTALLED.DB MANAGEMENT ====================

func installedDBPath() string {
	return filepath.Join(InstalledDir, "installed.db")
}

func readInstalledDB() map[string]InstalledEntry {
	result := make(map[string]InstalledEntry)
	data, err := os.ReadFile(installedDBPath())
	if err != nil {
		// Fall back to scanning .lst.gz files
		files, err2 := os.ReadDir(InstalledDir)
		if err2 != nil {
			return result
		}
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".lst.gz") {
				name := strings.TrimSuffix(f.Name(), ".lst.gz")
				result[name] = InstalledEntry{Archive: name + ".tar.xz", Explicit: 1}
			}
		}
		return result
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "INSTALLED.DB") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			explicit := 1
			if len(parts) >= 3 {
				explicit, _ = strconv.Atoi(parts[2])
			}
			result[parts[0]] = InstalledEntry{Archive: parts[1], Explicit: explicit}
		}
	}
	return result
}

func writeInstalledDB(db map[string]InstalledEntry) {
	os.MkdirAll(InstalledDir, 0755)

	names := make([]string, 0, len(db))
	for name := range db {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("INSTALLED.DB 3\n")
	for _, name := range names {
		entry := db[name]
		fmt.Fprintf(&sb, "%s %s %d\n", name, entry.Archive, entry.Explicit)
	}

	os.WriteFile(installedDBPath(), []byte(sb.String()), 0644)
}

func addToInstalledDB(name, archive string, explicit int) {
	db := readInstalledDB()
	db[name] = InstalledEntry{Archive: archive, Explicit: explicit}
	writeInstalledDB(db)
}

func removeFromInstalledDB(name string) {
	db := readInstalledDB()
	delete(db, name)
	writeInstalledDB(db)
}

func isInstalled(name string) bool {
	db := readInstalledDB()
	if _, ok := db[name]; ok {
		return true
	}
	// Fallback: check .lst.gz file directly
	_, err := os.Stat(filepath.Join(InstalledDir, name+".lst.gz"))
	return err == nil
}

func recordInstall(name, archive string, explicit int) {
	addToInstalledDB(name, archive, explicit)
}

// ==================== POSTINSTALL / PREREMOVE ====================

func runAllPostinstall() {
	postinstDir := filepath.Join(CygwinRoot, "etc", "postinstall")
	entries, err := os.ReadDir(postinstDir)
	if err != nil {
		return
	}

	bashExe := filepath.Join(CygwinRoot, "bin", "bash.exe")
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sh") && !strings.HasSuffix(name, ".dash") {
			continue
		}
		script := filepath.Join(postinstDir, name)
		fmt.Printf("Running %s\n", script)

		var cmd *exec.Cmd
		cygScript := toCygwinPath(script)
		if strings.HasSuffix(name, ".dash") {
			dashExe := filepath.Join(CygwinRoot, "bin", "dash.exe")
			cmd = exec.Command(dashExe, cygScript)
		} else {
			cmd = exec.Command(bashExe, "--login", cygScript)
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
		os.Rename(script, script+".done")
	}
}

func runPreremove(name string) {
	script := filepath.Join(CygwinRoot, "etc", "preremove", name+".sh")
	if _, err := os.Stat(script); err == nil {
		fmt.Printf("Running pre-remove script for %s...\n", name)
		bashExe := filepath.Join(CygwinRoot, "bin", "bash.exe")
		cmd := exec.Command(bashExe, "--login", toCygwinPath(script))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
		os.Remove(script)
	}
}

// ==================== HOLLOW / PE-HEADER CHECKS ====================

// checkHollow inspects an archive for hollow/stub characteristics.
// Returns nil if the archive looks legitimate, error if it appears hollow.
func checkHollow(pkg, archivePath string) error {
	info, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("cannot stat archive: %v", err)
	}
	size := info.Size()

	if size < hollowMinBytes {
		// Peek inside: if it's a valid tar with no .dll/.exe entries it may be
		// a meta-package (safe). Only flag archives that are tiny AND binary-named.
		bashExe := filepath.Join(CygwinRoot, "bin", "bash.exe")
		cygSrc := toCygwinPath(archivePath)
		out, _ := exec.Command(bashExe, "--login", "-c",
			fmt.Sprintf("tar -tf '%s' 2>/dev/null | grep -ciE '\\.(dll|exe)$' || echo 0", cygSrc)).Output()
		binCount := 0
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &binCount)

		if binCount == 0 {
			// Valid meta-package with no binaries
			return nil
		}

		msg := fmt.Sprintf("*** HOLLOW PACKAGE: %s ***\n"+
			"    Archive: %s\n"+
			"    Size:    %d bytes (threshold: %d)\n"+
			"    The archive passed hash verification because setup.ini records\n"+
			"    the hash of the stub itself — there is no usable payload.\n"+
			"    Try: apt-cyg listall %s",
			pkg, filepath.Base(archivePath), size, hollowMinBytes, pkg)

		if opts.AllowHollow {
			fmt.Fprintln(os.Stderr, msg)
			fmt.Fprintln(os.Stderr, "    (--allow-hollow set; proceeding anyway)")
			return nil
		}
		return fmt.Errorf("%s", msg)
	}

	if size < hollowWarnBytes {
		fmt.Fprintf(os.Stderr, "WARNING: %s archive is small (%s), verifying contents...\n",
			pkg, humanSize(size))

		// Count non-directory entries
		bashExe := filepath.Join(CygwinRoot, "bin", "bash.exe")
		cygSrc := toCygwinPath(archivePath)
		out, _ := exec.Command(bashExe, "--login", "-c",
			fmt.Sprintf("tar -tf '%s' 2>/dev/null | grep -vc '/$' || echo 0", cygSrc)).Output()
		realFiles := 0
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &realFiles)

		if realFiles == 0 {
			msg := fmt.Sprintf("*** HOLLOW PACKAGE: %s — archive contains only directories ***", pkg)
			if opts.AllowHollow {
				fmt.Fprintln(os.Stderr, msg)
				fmt.Fprintln(os.Stderr, "    (--allow-hollow set; proceeding anyway)")
				return nil
			}
			return fmt.Errorf("%s", msg)
		}
	}

	return nil
}

// checkPEBins verifies the MZ (PE) magic header of every .dll and .exe
// installed by a package. Returns (bad count, total checked).
func checkPEBins(pkg string) (bad, total int) {
	lstFile := filepath.Join(InstalledDir, pkg+".lst.gz")
	files := readFileList(lstFile)

	for _, entry := range files {
		if strings.HasSuffix(entry, "/") {
			continue
		}
		lower := strings.ToLower(entry)
		if !strings.HasSuffix(lower, ".dll") && !strings.HasSuffix(lower, ".exe") {
			continue
		}
		total++

		fullPath := "/" + strings.TrimPrefix(entry, "./")
		f, err := os.Open(fullPath)
		if err != nil {
			continue // missing file caught by check 3
		}
		header := make([]byte, 2)
		n, _ := f.Read(header)
		f.Close()

		if n < 2 || header[0] != 0x4D || header[1] != 0x5A {
			fmt.Fprintf(os.Stderr, "  GHOST BINARY: /%s (expected MZ header, got: %X)\n",
				strings.TrimPrefix(entry, "./"), header[:n])
			bad++
		}
	}
	return bad, total
}

// ==================== HASH VERIFICATION ====================

// verifyHash checks SHA-512 or MD5 of a downloaded file.
func verifyHash(pkg, path, sha512hash, md5hash string) error {
	if sha512hash != "" {
		fmt.Printf("Verifying sha512sum  %s ... ", filepath.Base(path))
		actual := sha512sumFile(path)
		if actual == sha512hash {
			fmt.Println("OK")
			return nil
		}
		return fmt.Errorf("sha512sum FAILED for %s\n  Expected: %s\n  Got:      %s",
			filepath.Base(path), sha512hash, actual)
	}
	if md5hash != "" {
		// MD5 verification via bash md5sum
		fmt.Printf("Verifying md5sum     %s ... ", filepath.Base(path))
		bashExe := filepath.Join(CygwinRoot, "bin", "bash.exe")
		out, err := exec.Command(bashExe, "--login", "-c",
			fmt.Sprintf("md5sum '%s' | awk '{print $1}'", toCygwinPath(path))).Output()
		if err != nil {
			return fmt.Errorf("md5sum error: %v", err)
		}
		actual := strings.TrimSpace(string(out))
		if actual == md5hash {
			fmt.Println("OK")
			return nil
		}
		return fmt.Errorf("md5sum FAILED for %s\n  Expected: %s\n  Got:      %s",
			filepath.Base(path), md5hash, actual)
	}
	return nil // no hash available, skip verification
}

func sha512sumFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha512.New()
	io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ==================== SETUP.INI PARSER ====================

func parseSetupIni() (map[string]Package, error) {
	if _, err := os.Stat(SetupIni); os.IsNotExist(err) {
		return nil, fmt.Errorf("setup.ini not found. Run 'apt-cyg update' first")
	}

	f, err := os.Open(SetupIni)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	packages := make(map[string]Package)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // larger buffer for long lines

	var cur *Package

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "@ ") {
			if cur != nil {
				packages[cur.Name] = *cur
			}
			cur = &Package{Name: strings.TrimPrefix(line, "@ ")}
			continue
		}

		if cur == nil {
			continue
		}

		switch {
		case strings.HasPrefix(line, "version: "):
			cur.Version = strings.TrimPrefix(line, "version: ")

		case strings.HasPrefix(line, "install: "):
			parts := strings.Fields(strings.TrimPrefix(line, "install: "))
			if len(parts) > 0 {
				cur.Install = parts[0]
			}
			if len(parts) > 1 {
				cur.Size, _ = strconv.ParseInt(parts[1], 10, 64)
			}
			if len(parts) > 2 {
				hash := parts[2]
				switch len(hash) {
				case 32:
					cur.MD5 = hash
				case 128:
					cur.SHA512 = hash
				}
			}

		case strings.HasPrefix(line, "depends2:"):
			// Modern format: takes priority over depends: and requires:
			raw := strings.TrimSpace(line[len("depends2:"):])
			raw = versionConstraintRe.ReplaceAllString(raw, "")
			cur.Depends = []string{raw}

		case strings.HasPrefix(line, "depends:"):
			// Only use if depends2: not already found for this package
			if len(cur.Depends) == 0 {
				raw := strings.TrimSpace(line[len("depends:"):])
				raw = versionConstraintRe.ReplaceAllString(raw, "")
				cur.Depends = []string{raw}
			}

		case strings.HasPrefix(line, "requires: "):
			// Legacy format: space-separated, no version constraints
			if len(cur.Depends) == 0 {
				cur.Depends = []string{strings.TrimPrefix(line, "requires: ")}
			}

		case strings.HasPrefix(line, "provides: "):
			raw := strings.TrimPrefix(line, "provides: ")
			for _, p := range strings.Split(raw, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					cur.Provides = append(cur.Provides, p)
				}
			}

		case strings.HasPrefix(line, "category: "):
			cur.Category = strings.TrimPrefix(line, "category: ")

		case strings.HasPrefix(line, "sdesc: "):
			cur.Description = strings.Trim(strings.TrimPrefix(line, "sdesc: "), `"`)
		}
	}

	if cur != nil {
		packages[cur.Name] = *cur
	}

	return packages, scanner.Err()
}

// ==================== HELPERS ====================

func cleanDepName(dep string) string {
	dep = strings.TrimSpace(dep)
	dep = strings.TrimSuffix(dep, ",")

	if dep == "" || dep == "(" || dep == ")" {
		return ""
	}
	// Skip virtual packages starting with underscore
	if strings.HasPrefix(dep, "_") {
		return ""
	}
	// Skip version operators and bare version numbers
	if strings.HasPrefix(dep, ">") || strings.HasPrefix(dep, "<") ||
		strings.HasPrefix(dep, "=") || strings.HasPrefix(dep, "!") ||
		(len(dep) > 0 && dep[0] >= '0' && dep[0] <= '9') {
		return ""
	}
	return dep
}

// readFileList decompresses a .lst.gz manifest and returns file paths.
func readFileList(lstFile string) []string {
	f, err := os.Open(lstFile)
	if err != nil {
		return nil
	}
	defer f.Close()

	var reader io.Reader
	gz, err := gzip.NewReader(f)
	if err != nil {
		// Not gzipped, try as plain text
		f.Seek(0, io.SeekStart)
		reader = f
	} else {
		defer gz.Close()
		reader = gz
	}

	var lines []string
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func downloadWithProgress(u, dest string, expectedSize int64) error {
	resp, err := httpGet(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}

	if expectedSize <= 0 && resp.ContentLength > 0 {
		expectedSize = resp.ContentLength
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}

	counter := &progressCounter{Expected: expectedSize}
	_, err = io.Copy(out, io.TeeReader(resp.Body, counter))
	out.Close()
	fmt.Println()
	if err != nil {
		os.Remove(dest)
	}
	return err
}

type progressCounter struct {
	Total    int64
	Expected int64
}

func (c *progressCounter) Write(p []byte) (int, error) {
	n := len(p)
	c.Total += int64(n)
	if c.Expected > 0 {
		pct := c.Total * 100 / c.Expected
		bars := int(c.Total * 40 / c.Expected)
		if bars > 40 {
			bars = 40
		}
		bar := strings.Repeat("=", bars) + strings.Repeat(" ", 40-bars)
		fmt.Printf("\r  [%s] %3d%%  %s", bar, pct, humanSize(c.Total))
	} else {
		fmt.Printf("\r  %s", humanSize(c.Total))
	}
	return n, nil
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f kB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
	}
}

func toCygwinPath(winPath string) string {
	// Convert to Cygwin /cygdrive/<drive>/... format.
	// Mixed-style "C:/path" causes tar to interpret "C:" as a remote hostname
	// and fail with "Cannot connect to C: resolve failed".
	// /cygdrive is a hard-coded Cygwin mount and always accessible even when
	// bash.exe is spawned from a native Windows process.
	if len(winPath) >= 2 && winPath[1] == ':' {
		drive := strings.ToLower(string(winPath[0]))
		rest := strings.ReplaceAll(winPath[2:], `\`, "/")
		return "/cygdrive/" + drive + rest
	}
	return strings.ReplaceAll(winPath, `\`, "/")
}

func toWindowsPath(cygPath string) (string, error) {
	cygpathExe := filepath.Join(CygwinRoot, "bin", "cygpath.exe")
	out, err := exec.Command(cygpathExe, "-w", cygPath).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
}
