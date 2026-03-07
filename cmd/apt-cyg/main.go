package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	CygwinRoot   = `C:\cygwin64`
	CacheDir     = CygwinRoot + `\var\cache\apt-cyg`
	SetupIni     = CacheDir + `\setup.ini`
	InstalledDir = CygwinRoot + `\etc\setup`
	Version      = "1.1.0"
)

var (
	DefaultMirror = "https://mirrors.kernel.org/sourceware/cygwin"
	CurrentMirror = DefaultMirror
)

type Package struct {
	Name        string
	Version     string
	Install     string // download URL
	Depends     []string
	Provides    []string
	Category    string
	Description string
}

func main() {
	// Detect if called as "apt" (alias mode)
	execName := strings.ToLower(filepath.Base(os.Args[0]))
	isAptAlias := execName == "apt.exe" || execName == "apt"

	if len(os.Args) < 2 {
		showHelp(isAptAlias)
		os.Exit(0)
	}

	// Ensure cache directory exists
	os.MkdirAll(CacheDir, 0755)

	command := os.Args[1]
	args := os.Args[2:]

	// apt alias compatibility: map apt-get commands to apt-cyg equivalents
	if isAptAlias {
		command = mapAptCommand(command)
	}

	switch command {
	case "update":
		cmdUpdate()
	case "install":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No package specified")
			os.Exit(1)
		}
		cmdInstall(args)
	case "remove", "uninstall", "purge":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No package specified")
			os.Exit(1)
		}
		cmdRemove(args)
	case "search":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No search term specified")
			os.Exit(1)
		}
		cmdSearch(args[0])
	case "list":
		cmdList(args)
	case "show", "info":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No package specified")
			os.Exit(1)
		}
		cmdShow(args[0])
	case "depends":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No package specified")
			os.Exit(1)
		}
		cmdDepends(args[0])
	case "rdepends":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No package specified")
			os.Exit(1)
		}
		cmdRdepends(args[0])
	case "upgrade":
		cmdUpgrade(args)
	case "download":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No package specified")
			os.Exit(1)
		}
		cmdDownload(args)
	case "mirror":
		if len(args) == 0 {
			fmt.Println(CurrentMirror)
		} else {
			CurrentMirror = args[0]
			fmt.Printf("Mirror set to: %s\n", CurrentMirror)
		}
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
		fmt.Printf("%s version %s (Go)\n", name, Version)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}

// mapAptCommand maps apt/apt-get commands to apt-cyg equivalents
func mapAptCommand(cmd string) string {
	mapping := map[string]string{
		"get":        "install", // apt get -> apt install (deprecated)
		"autoclean":  "clean",
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
	help := name + ` - package manager for Cygwin (Go version)

Usage: ` + name + ` <command> [arguments]

Commands:
  update              Download fresh package list from mirror
  install <pkg...>    Install package(s) with dependencies
  remove <pkg...>     Remove package(s)
  search <pattern>    Search for packages matching pattern
  list [--installed]  List packages (use --installed for installed only)
  show <package>      Show detailed package information
  depends <package>   Show package dependencies
  rdepends <package>  Show reverse dependencies (what depends on this)
  upgrade [pkg...]    Upgrade installed packages (all or specified)
  download <pkg...>   Download packages without installing
  autoremove          Remove unused dependencies
  clean               Clear package cache
  mirror [url]        Set or show current mirror

Options:
  --help, -h          Show this help
  --version, -v       Show version

Examples:
  ` + name + ` update                    Update package list
  ` + name + ` install vim git          Install vim and git
  ` + name + ` search python            Search for python packages
  ` + name + ` list --installed         List all installed packages
  ` + name + ` upgrade                  Upgrade all packages
  ` + name + ` remove vim               Remove vim
`
	fmt.Print(help)
}

// ==================== UPDATE ====================

func cmdUpdate() {
	fmt.Println("Updating package list...")

	// Download setup.ini (uncompressed)
	url := CurrentMirror + "/x86_64/setup.ini"
	fmt.Printf("Downloading from %s\n", url)

	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to download: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	// Save to file
	out, err := os.Create(SetupIni)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to create file: %v\n", err)
		os.Exit(1)
	}

	// Progress
	counter := &writeCounter{}
	_, err = io.Copy(out, io.TeeReader(resp.Body, counter))
	fmt.Println()
	out.Close()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to save file: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Update complete.")
}

type writeCounter struct {
	Total uint64
}

func (wc *writeCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.Total += uint64(n)
	wc.PrintProgress()
	return n, nil
}

func (wc *writeCounter) PrintProgress() {
	fmt.Printf("\rDownloading... %d MB", wc.Total/1024/1024)
}

// ==================== HELPERS ====================

func decompressGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, gz)
	return err
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

	for name, pkg := range packages {
		if re.MatchString(name) || re.MatchString(pkg.Description) {
			installed := isInstalled(name)
			status := " "
			if installed {
				status = "i"
			}
			fmt.Printf("[%s] %-30s %s\n", status, name, pkg.Description)
		}
	}
}

// ==================== LIST ====================

func cmdList(args []string) {
	installedOnly := false
	for _, arg := range args {
		if arg == "--installed" || arg == "-i" {
			installedOnly = true
		}
	}

	if installedOnly {
		// List installed packages only
		files, err := os.ReadDir(InstalledDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".lst.gz") {
				name := strings.TrimSuffix(f.Name(), ".lst.gz")
				fmt.Println(name)
			}
		}
	} else {
		// List all available packages
		packages, err := parseSetupIni()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		for name, pkg := range packages {
			status := " "
			if isInstalled(name) {
				status = "i"
			}
			fmt.Printf("[%s] %-30s %s\n", status, name, pkg.Description)
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
		fmt.Fprintf(os.Stderr, "Package not found: %s\n", name)
		os.Exit(1)
	}

	fmt.Printf("Package: %s\n", pkg.Name)
	fmt.Printf("Version: %s\n", pkg.Version)
	fmt.Printf("Category: %s\n", pkg.Category)
	if len(pkg.Depends) > 0 {
		fmt.Printf("Depends: %s\n", strings.Join(pkg.Depends, ", "))
	}
	if len(pkg.Provides) > 0 {
		fmt.Printf("Provides: %s\n", strings.Join(pkg.Provides, ", "))
	}
	fmt.Printf("Description: %s\n", pkg.Description)
	fmt.Printf("Installed: %v\n", isInstalled(name))
}

// ==================== DEPENDS ====================

func cmdDepends(name string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	pkg, ok := packages[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "Package not found: %s\n", name)
		os.Exit(1)
	}

	fmt.Printf("Dependencies for %s:\n", name)
	printDependencies(pkg.Depends, 0, packages)
}

func printDependencies(deps []string, level int, packages map[string]Package) {
	indent := strings.Repeat("  ", level)
	for _, depLine := range deps {
		depList := strings.Split(depLine, ",")
		for _, dep := range depList {
			depName := cleanDepName(dep)
			if depName == "" {
				continue
			}
			installed := isInstalled(depName)
			status := " "
			if installed {
				status = "i"
			}
			fmt.Printf("%s[%s] %s\n", indent, status, depName)

			// Recursively show dependencies (limit depth to avoid infinite loops)
			if level < 2 {
				if subPkg, ok := packages[depName]; ok && len(subPkg.Depends) > 0 {
					printDependencies(subPkg.Depends, level+1, packages)
				}
			}
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

	_, ok := packages[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "Package not found: %s\n", name)
		os.Exit(1)
	}

	fmt.Printf("Reverse dependencies for %s (packages that depend on it):\n", name)

	// Find all packages that depend on this one
	for pkgName, pkg := range packages {
		for _, depLine := range pkg.Depends {
			depList := strings.Split(depLine, ",")
			for _, dep := range depList {
				depName := cleanDepName(dep)
				if depName == name {
					installed := isInstalled(pkgName)
					status := " "
					if installed {
						status = "i"
					}
					fmt.Printf("[%s] %s - %s\n", status, pkgName, pkg.Description)
					break
				}
			}
		}
	}
}

// ==================== UPGRADE ====================

func cmdUpgrade(names []string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Get list of packages to upgrade
	toUpgrade := []string{}
	if len(names) > 0 {
		// Upgrade specific packages
		for _, name := range names {
			if !isInstalled(name) {
				fmt.Printf("%s is not installed, skipping.\n", name)
				continue
			}
			toUpgrade = append(toUpgrade, name)
		}
	} else {
		// Upgrade all installed packages
		files, err := os.ReadDir(InstalledDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".lst.gz") {
				name := strings.TrimSuffix(f.Name(), ".lst.gz")
				toUpgrade = append(toUpgrade, name)
			}
		}
	}

	if len(toUpgrade) == 0 {
		fmt.Println("No packages to upgrade.")
		return
	}

	fmt.Printf("Will upgrade %d package(s): %s\n", len(toUpgrade), strings.Join(toUpgrade, " "))

	// Reinstall packages (this will download latest version)
	for _, name := range toUpgrade {
		pkg, ok := packages[name]
		if !ok {
			fmt.Printf("Warning: %s not found in repository, skipping.\n", name)
			continue
		}
		installPackage(name, pkg)
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

		cacheFile := filepath.Join(CacheDir, filepath.Base(pkg.Install))

		// Check if already downloaded
		if _, err := os.Stat(cacheFile); err == nil {
			fmt.Printf("%s already downloaded: %s\n", name, cacheFile)
			continue
		}

		// Download
		url := CurrentMirror + "/" + pkg.Install
		fmt.Printf("Downloading %s from %s\n", name, url)

		resp, err := http.Get(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to download %s: %v\n", name, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "Error: HTTP %d for %s\n", resp.StatusCode, name)
			continue
		}

		out, err := os.Create(cacheFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to create file: %v\n", err)
			continue
		}

		counter := &writeCounter{}
		_, err = io.Copy(out, io.TeeReader(resp.Body, counter))
		fmt.Println()
		out.Close()

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to save %s: %v\n", name, err)
			continue
		}

		fmt.Printf("%s downloaded to %s\n", name, cacheFile)
	}
}

// ==================== AUTOREMOVE ====================

func cmdAutoremove() {
	fmt.Println("Finding unused dependencies...")

	// Get all installed packages
	installed := make(map[string]bool)
	files, err := os.ReadDir(InstalledDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".lst.gz") {
			name := strings.TrimSuffix(f.Name(), ".lst.gz")
			installed[name] = true
		}
	}

	// Find explicitly installed packages (those manually requested)
	// For now, we'll use a heuristic: packages in base category or with no dependencies are likely manual
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Build dependency graph
	needed := make(map[string]bool)
	for name := range installed {
		// Check if this package is a dependency of another installed package
		for otherName := range installed {
			if name == otherName {
				continue
			}
			pkg := packages[otherName]
			for _, depLine := range pkg.Depends {
				depList := strings.Split(depLine, ",")
				for _, dep := range depList {
					depName := cleanDepName(dep)
					if depName == name {
						needed[name] = true
					}
				}
			}
		}
	}

	// Find orphaned packages
	orphaned := []string{}
	for name := range installed {
		if !needed[name] {
			// Skip essential packages
			if name == "bash" || name == "coreutils" || name == "cygwin" ||
				strings.HasPrefix(name, "lib") {
				continue
			}
			orphaned = append(orphaned, name)
		}
	}

	if len(orphaned) == 0 {
		fmt.Println("No orphaned packages found.")
		return
	}

	fmt.Printf("Orphaned packages (%d): %s\n", len(orphaned), strings.Join(orphaned, " "))
	fmt.Println("Use 'apt-cyg remove <package>' to remove them.")
}

// ==================== CLEAN ====================

func cmdClean() {
	fmt.Println("Cleaning package cache...")

	files, err := os.ReadDir(CacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Keep setup.ini, remove package archives
	removed := 0
	var totalSize int64
	for _, f := range files {
		if f.Name() == "setup.ini" {
			continue
		}
		filePath := filepath.Join(CacheDir, f.Name())
		info, _ := f.Info()
		totalSize += info.Size()
		if err := os.Remove(filePath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to remove %s: %v\n", f.Name(), err)
		} else {
			removed++
		}
	}

	fmt.Printf("Removed %d files, freed %d MB\n", removed, totalSize/1024/1024)
}

// ==================== INSTALL ====================

func cmdInstall(names []string) {
	packages, err := parseSetupIni()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Resolve all packages and dependencies
	toInstall := []string{}
	for _, name := range names {
		deps := resolveDependencies(name, packages, make(map[string]bool))
		toInstall = append(toInstall, deps...)
	}

	// Remove duplicates
	seen := make(map[string]bool)
	unique := []string{}
	for _, name := range toInstall {
		if !seen[name] && !isInstalled(name) {
			seen[name] = true
			unique = append(unique, name)
		}
	}

	if len(unique) == 0 {
		fmt.Println("All packages already installed.")
		return
	}

	fmt.Printf("Will install: %s\n", strings.Join(unique, " "))

	for _, name := range unique {
		installPackage(name, packages[name])
	}
}

func resolveDependencies(name string, packages map[string]Package, visited map[string]bool) []string {
	if visited[name] {
		return nil
	}
	visited[name] = true

	pkg, ok := packages[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "Warning: Package not found: %s\n", name)
		return nil
	}

	result := []string{name}
	for _, depLine := range pkg.Depends {
		// Dependencies are comma-separated: "bash, libncursesw10, libgcc1"
		deps := strings.Split(depLine, ",")
		for _, dep := range deps {
			depName := cleanDepName(dep)
			if depName == "" {
				continue
			}
			subDeps := resolveDependencies(depName, packages, visited)
			result = append(result, subDeps...)
		}
	}

	return result
}

func cleanDepName(dep string) string {
	// Dependencies are comma-separated: "bash, libncursesw10, libgcc1"
	// Also handle space after comma
	dep = strings.TrimSpace(dep)
	dep = strings.TrimSuffix(dep, ",")

	// Skip empty, special chars, version constraints
	if dep == "" || dep == "(" || dep == ")" {
		return ""
	}

	// Skip special virtual packages like _windows
	if strings.HasPrefix(dep, "_") {
		return ""
	}

	// Skip version operators
	if strings.HasPrefix(dep, ">") || strings.HasPrefix(dep, "<") ||
		strings.HasPrefix(dep, "=") || strings.HasPrefix(dep, "!") ||
		regexp.MustCompile(`^[0-9]`).MatchString(dep) {
		return ""
	}

	return dep
}

func installPackage(name string, pkg Package) {
	fmt.Printf("\nInstalling %s...\n", name)

	// Download
	cacheFile := filepath.Join(CacheDir, filepath.Base(pkg.Install))
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		url := CurrentMirror + "/" + pkg.Install
		fmt.Printf("Downloading %s\n", url)

		resp, err := http.Get(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to download: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		out, err := os.Create(cacheFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to create file: %v\n", err)
			os.Exit(1)
		}
		_, err = io.Copy(out, resp.Body)
		out.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to save file: %v\n", err)
			os.Exit(1)
		}
	}

	// Extract
	fmt.Println("Extracting...")
	if err := extractTarXz(cacheFile, CygwinRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to extract: %v\n", err)
		os.Exit(1)
	}

	// Record installation
	recordInstall(name)

	// Run postinstall
	runPostinstall(name)

	fmt.Printf("%s installed.\n", name)
}

func extractTarXz(src, dst string) error {
	// Convert Windows paths to Cygwin paths
	cygSrc := toCygwinPath(src)
	cygDst := toCygwinPath(dst)

	// Use Cygwin bash to execute tar
	tarCmd := fmt.Sprintf("tar -xJf '%s' -C '%s'", cygSrc, cygDst)
	cmd := exec.Command(filepath.Join(CygwinRoot, "bin", "bash.exe"), "-c", tarCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func toCygwinPath(winPath string) string {
	// Convert Windows path to Cygwin path
	// C:\cygwin64\... -> /c/cygwin64/... (Git Bash style)
	// This works because Cygwin mounts C: on /c
	cygPath := strings.ReplaceAll(winPath, `\`, "/")
	if len(cygPath) >= 2 && cygPath[1] == ':' {
		cygPath = "/" + strings.ToLower(string(cygPath[0])) + cygPath[2:]
	}
	return cygPath
}

func recordInstall(name string) {
	// Create .lst.gz file
	lstFile := filepath.Join(InstalledDir, name+".lst.gz")
	// Create empty file for now (full implementation would list files)
	os.WriteFile(lstFile, []byte{}, 0644)
}

func runPostinstall(name string) {
	postinst := filepath.Join(CygwinRoot, "etc", "postinstall", name+".sh")
	if _, err := os.Stat(postinst); err == nil {
		fmt.Printf("Running postinstall for %s...\n", name)
		cmd := exec.Command(filepath.Join(CygwinRoot, "bin", "bash.exe"), "-c", postinst)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()

		// Mark as done
		doneFile := postinst + ".done"
		os.Rename(postinst, doneFile)
	}
}

// ==================== REMOVE ====================

func cmdRemove(names []string) {
	for _, name := range names {
		if !isInstalled(name) {
			fmt.Printf("%s is not installed.\n", name)
			continue
		}

		fmt.Printf("Removing %s...\n", name)

		// Read file list
		lstFile := filepath.Join(InstalledDir, name+".lst.gz")
		files := readFileList(lstFile)

		// Remove files
		for _, f := range files {
			fullPath := filepath.Join(CygwinRoot, f)
			os.Remove(fullPath)
		}

		// Remove record
		os.Remove(lstFile)

		fmt.Printf("%s removed.\n", name)
	}
}

func readFileList(lstFile string) []string {
	// Use zcat to decompress
	cmd := exec.Command("zcat", lstFile)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	lines := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines
}

// ==================== HELPERS ====================

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

	var currentPkg *Package

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "@ ") {
			// Save previous package
			if currentPkg != nil {
				packages[currentPkg.Name] = *currentPkg
			}
			// Start new package
			currentPkg = &Package{
				Name: strings.TrimPrefix(line, "@ "),
			}
		} else if currentPkg != nil {
			if strings.HasPrefix(line, "version: ") {
				currentPkg.Version = strings.TrimPrefix(line, "version: ")
			} else if strings.HasPrefix(line, "install: ") {
				parts := strings.Fields(strings.TrimPrefix(line, "install: "))
				if len(parts) > 0 {
					currentPkg.Install = parts[0]
				}
			} else if strings.HasPrefix(line, "depends2:") {
				// depends2: cygwin, libiconv2, libintl8
				deps := strings.TrimPrefix(line, "depends2:")
				deps = strings.TrimSpace(deps)
				// Split by comma, store as single line for later processing
				currentPkg.Depends = []string{deps}
			} else if strings.HasPrefix(line, "category: ") {
				currentPkg.Category = strings.TrimPrefix(line, "category: ")
			} else if strings.HasPrefix(line, "sdesc: ") {
				currentPkg.Description = strings.Trim(strings.TrimPrefix(line, "sdesc: "), "\"")
			}
		}
	}

	// Save last package
	if currentPkg != nil {
		packages[currentPkg.Name] = *currentPkg
	}

	return packages, scanner.Err()
}

func isInstalled(name string) bool {
	lstFile := filepath.Join(InstalledDir, name+".lst.gz")
	_, err := os.Stat(lstFile)
	return err == nil
}

func init() {
	// Detect number of CPUs for parallel operations
	runtime.GOMAXPROCS(runtime.NumCPU())
}
