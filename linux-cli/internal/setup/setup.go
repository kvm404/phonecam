// Package setup implements `phonecam setup`: install Linux receive deps and
// persist an OBS-aware v4l2loopback PhoneCam node. It requires root unless
// --dry-run. It does not ship distro packages or auto-modprobe from postinst.
package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kvm404/phonecam/linux-cli/internal/doctor"
	"github.com/kvm404/phonecam/linux-cli/internal/v4l2"
)

const (
	DevicePath          = "/dev/video10"
	ModprobeConfPath    = "/etc/modprobe.d/phonecam.conf"
	ModulesLoadPath     = "/etc/modules-load.d/phonecam.conf"
	OBSModprobeConfPath = "/etc/modprobe.d/v4l2loopback.conf"
	videoGroup          = "video"
	controlPortProto    = "47470/tcp"
	rtpPortProto        = "47471/udp"
)

// ErrNeedRoot is returned when setup is asked to change the system without root.
var ErrNeedRoot = errors.New("phonecam setup requires root; run: sudo phonecam setup")

// FedoraFusionMessage is the doctor-style line used when RPM Fusion is missing
// and cannot be enabled. v4l2loopback and gst-libav are not in stock Fedora.
const FedoraFusionMessage = "v4l2loopback and gst-libav are not in stock Fedora; enable RPM Fusion (https://rpmfusion.org/Configuration) then re-run: sudo phonecam setup"

const (
	loopbackLoad      = "load"
	loopbackLeave     = "leave"
	loopbackReloadOBS = "reload-obs"
	loopbackReload    = "reload"
)

// System is the OS surface setup needs. Tests fake it so unit tests never require root.
type System interface {
	Geteuid() int
	Getenv(name string) string
	UnameRelease() (string, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Exists(path string) bool
	Run(name string, args ...string) error
	Output(name string, args ...string) ([]byte, error)
	DeviceOpeners(path string) ([]string, error)
}

// OSSystem implements System against the real OS.
type OSSystem struct{}

func (OSSystem) Geteuid() int { return os.Geteuid() }

func (OSSystem) Getenv(name string) string { return os.Getenv(name) }

func (OSSystem) UnameRelease() (string, error) {
	out, err := exec.Command("uname", "-r").Output()
	return strings.TrimSpace(string(out)), err
}

func (OSSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (OSSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (OSSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSSystem) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (OSSystem) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (OSSystem) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func (OSSystem) DeviceOpeners(path string) ([]string, error) {
	return deviceOpeners(path)
}

const maxDeviceOpeners = 8

func deviceOpeners(path string) ([]string, error) {
	resolved, _ := filepath.EvalSymlinks(path)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	seen := make(map[string]struct{})
	var names []string
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		holds := false
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if link == path || (resolved != "" && link == resolved) {
				holds = true
				break
			}
		}
		if !holds {
			continue
		}
		name := strings.TrimSpace(string(readProcFile(filepath.Join("/proc", entry.Name(), "comm"))))
		if name == "" {
			name = "pid:" + entry.Name()
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
		if len(names) >= maxDeviceOpeners {
			break
		}
	}
	return names, nil
}

func readProcFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

// Config is the user-facing setup options.
type Config struct {
	DryRun bool
}

// Loopback is the planned v4l2loopback load/reload.
type Loopback struct {
	Kind    string
	Rmmod   bool
	Args    []string
	Persist string
}

// Plan is the computed setup action list. Dry-run prints it; apply executes it.
type Plan struct {
	Family           string
	Packages         []string
	PreCmds          [][]string
	PackageCmd       []string
	RPMFusion        bool
	Loopback         Loopback
	WriteModprobe    bool
	ModprobeBody     string
	WriteModulesLoad bool
	ModulesLoadBody  string
	GroupUser        string
	FirewallCmds     [][]string
	Warnings         []string
}

// Run plans setup and either prints the plan (--dry-run) or applies it as root.
func Run(sys System, cfg Config, stdout, stderr io.Writer) error {
	if sys == nil {
		sys = OSSystem{}
	}
	if !cfg.DryRun && sys.Geteuid() != 0 {
		return ErrNeedRoot
	}

	plan, err := BuildPlan(sys)
	if err != nil {
		return err
	}

	if cfg.DryRun {
		fmt.Fprintln(stdout, "PhoneCam setup (dry-run)")
		fmt.Fprintln(stdout)
		for _, line := range plan.Lines() {
			fmt.Fprintln(stdout, line)
		}
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "No changes were made.")
		return nil
	}

	return Apply(sys, plan, stdout)
}

// BuildPlan inspects the system and returns the actions setup would take.
func BuildPlan(sys System) (Plan, error) {
	family := doctor.DistroFamily(sys)
	uname, _ := sys.UnameRelease()

	plan := Plan{
		Family:           family,
		WriteModulesLoad: true,
		ModulesLoadBody:  "v4l2loopback\n",
	}

	pkgs, pre, install, rpmFusion, err := packagePlan(sys, family, uname)
	if err != nil {
		return Plan{}, err
	}
	plan.Packages = pkgs
	plan.PreCmds = pre
	plan.PackageCmd = install
	plan.RPMFusion = rpmFusion

	loop, err := planLoopback(sys)
	if err != nil {
		return Plan{}, err
	}
	plan.Loopback = loop
	plan.ModprobeBody = loop.Persist + "\n"

	if sys.Exists(OBSModprobeConfPath) {
		plan.WriteModprobe = false
		plan.Warnings = append(plan.Warnings,
			"existing "+OBSModprobeConfPath+" is not overwritten (OBS-owned); not writing "+ModprobeConfPath+" to avoid conflicting module options at boot")
	} else {
		plan.WriteModprobe = loop.Persist != ""
	}

	if user := invokingUser(sys); user != "" {
		if !userInGroup(sys, user, videoGroup) {
			if groupExists(sys, videoGroup) {
				plan.GroupUser = user
			} else {
				plan.Warnings = append(plan.Warnings, "group "+videoGroup+" does not exist; skip usermod")
			}
		}
	}

	plan.FirewallCmds = firewallPlan(sys)
	return plan, nil
}

func packagePlan(sys System, family, uname string) (pkgs []string, pre [][]string, install []string, rpmFusion bool, err error) {
	switch family {
	case "arch":
		headers := archHeaderPackage(sys, uname)
		pkgs = []string{
			"gstreamer", "gst-plugins-base", "gst-plugins-good", "gst-plugins-bad",
			"gst-libav", "v4l2loopback-dkms", headers,
		}
		install = append([]string{"pacman", "-S", "--needed", "--noconfirm"}, pkgs...)
		return pkgs, nil, install, false, nil
	case "fedora":
		pkgs = []string{
			"gstreamer1", "gstreamer1-plugins-base", "gstreamer1-plugins-good",
			"gstreamer1-plugins-bad-free", "gstreamer1-plugin-libav", "v4l2loopback",
		}
		install = append([]string{"dnf", "install", "-y"}, pkgs...)
		if !sys.Exists("/etc/yum.repos.d/rpmfusion-free.repo") {
			rpmFusion = true
			ver := fedoraVersion(sys)
			if ver == "" {
				return nil, nil, nil, true, errors.New(FedoraFusionMessage)
			}
			url := "https://mirrors.rpmfusion.org/free/fedora/rpmfusion-free-release-" + ver + ".noarch.rpm"
			pre = [][]string{{"dnf", "install", "-y", url}}
		}
		return pkgs, pre, install, rpmFusion, nil
	case "debian":
		if uname == "" {
			uname = "generic"
		}
		headers := "linux-headers-" + uname
		pkgs = []string{
			"gstreamer1.0-tools", "gstreamer1.0-plugins-base", "gstreamer1.0-plugins-good",
			"gstreamer1.0-plugins-bad", "gstreamer1.0-libav", "v4l2loopback-dkms", headers,
		}
		pre = [][]string{{"apt-get", "update"}}
		install = append([]string{"apt-get", "install", "-y"}, pkgs...)
		return pkgs, pre, install, false, nil
	case "unknown":
		return nil, nil, nil, false, fmt.Errorf("could not read /etc/os-release; use phonecam install for hints")
	default:
		return nil, nil, nil, false, fmt.Errorf("unsupported distro %s; use phonecam install for hints", family)
	}
}

func archHeaderPackage(sys System, uname string) string {
	if uname == "" {
		return "linux-headers"
	}
	data, err := sys.ReadFile("/usr/lib/modules/" + uname + "/pkgbase")
	if err != nil {
		return "linux-headers"
	}
	base := strings.TrimSpace(string(data))
	if base == "" {
		return "linux-headers"
	}
	return base + "-headers"
}

func fedoraVersion(sys System) string {
	data, err := sys.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	return osReleaseValue(string(data), "VERSION_ID")
}

func osReleaseValue(data, key string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || name != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

func planLoopback(sys System) (Loopback, error) {
	loaded := sys.Exists("/sys/module/v4l2loopback")
	video10 := sys.Exists(DevicePath)
	phoneCamOnly := Loopback{
		Kind:    loopbackLoad,
		Args:    []string{"video_nr=10", "card_label=PhoneCam", "exclusive_caps=1"},
		Persist: "options v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1",
	}

	if video10 {
		name := deviceLabel(sys, 10)
		if name != v4l2.ExpectedCardLabel {
			if name == "" {
				return Loopback{}, fmt.Errorf("/dev/video10 exists but its label could not be read; not stealing the node")
			}
			return Loopback{}, fmt.Errorf("/dev/video10 exists but is labeled %q, not PhoneCam; not stealing the node", name)
		}
		if loaded {
			if exclusiveCapsOn(sys, 10) {
				return Loopback{
					Kind:    loopbackLeave,
					Persist: persistOptions(sys),
				}, nil
			}
			return reloadCurrent(sys)
		}
		return Loopback{Kind: loopbackLeave, Persist: phoneCamOnly.Persist}, nil
	}

	if !loaded {
		return phoneCamOnly, nil
	}

	nrs := loadedVideoNumbers(sys)
	if containsInt(nrs, 0) && !containsInt(nrs, 10) {
		if err := ensureNoHolders(sys, nrs); err != nil {
			return Loopback{}, err
		}
		label0 := deviceLabel(sys, 0)
		if label0 == "" {
			label0 = "OBS Virtual Camera"
		}
		return Loopback{
			Kind:  loopbackReloadOBS,
			Rmmod: true,
			Args: []string{
				"devices=2",
				"video_nr=0,10",
				"card_label=" + label0 + ",PhoneCam",
				"exclusive_caps=1,1",
			},
			Persist: `options v4l2loopback devices=2 video_nr=0,10 card_label="` + label0 + `,PhoneCam" exclusive_caps=1,1`,
		}, nil
	}

	if err := ensureNoHolders(sys, nrs); err != nil {
		return Loopback{}, err
	}
	phoneCamOnly.Rmmod = len(nrs) > 0
	phoneCamOnly.Kind = loopbackReload
	return phoneCamOnly, nil
}

func reloadCurrent(sys System) (Loopback, error) {
	nrs := loadedVideoNumbers(sys)
	if err := ensureNoHolders(sys, nrs); err != nil {
		return Loopback{}, err
	}
	if containsInt(nrs, 0) && containsInt(nrs, 10) {
		label0 := deviceLabel(sys, 0)
		if label0 == "" {
			label0 = "OBS Virtual Camera"
		}
		return Loopback{
			Kind:  loopbackReloadOBS,
			Rmmod: true,
			Args: []string{
				"devices=2",
				"video_nr=0,10",
				"card_label=" + label0 + ",PhoneCam",
				"exclusive_caps=1,1",
			},
			Persist: `options v4l2loopback devices=2 video_nr=0,10 card_label="` + label0 + `,PhoneCam" exclusive_caps=1,1`,
		}, nil
	}
	return Loopback{
		Kind:    loopbackReload,
		Rmmod:   true,
		Args:    []string{"video_nr=10", "card_label=PhoneCam", "exclusive_caps=1"},
		Persist: "options v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1",
	}, nil
}

func persistOptions(sys System) string {
	nrs := loadedVideoNumbers(sys)
	if containsInt(nrs, 0) && containsInt(nrs, 10) {
		label0 := deviceLabel(sys, 0)
		if label0 == "" {
			label0 = "OBS Virtual Camera"
		}
		return `options v4l2loopback devices=2 video_nr=0,10 card_label="` + label0 + `,PhoneCam" exclusive_caps=1,1`
	}
	return "options v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1"
}

func exclusiveCapsOn(sys System, n int) bool {
	videoNr, err1 := sys.ReadFile(v4l2.VideoNrParameterPath)
	caps, err2 := sys.ReadFile(v4l2.ExclusiveCapsParameterPath)
	if err1 != nil || err2 != nil {
		return true
	}
	enabled, found := v4l2.ExclusiveCapsForDevice(string(videoNr), string(caps), n)
	if !found {
		return true
	}
	return enabled
}

func loadedVideoNumbers(sys System) []int {
	data, err := sys.ReadFile(v4l2.VideoNrParameterPath)
	if err != nil {
		return nil
	}
	var nrs []int
	for _, raw := range strings.Split(strings.TrimSpace(string(data)), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			continue
		}
		nrs = append(nrs, v)
	}
	return nrs
}

func deviceLabel(sys System, n int) string {
	data, err := sys.ReadFile("/sys/class/video4linux/video" + strconv.Itoa(n) + "/name")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func ensureNoHolders(sys System, nrs []int) error {
	var held []string
	seen := map[string]struct{}{}
	for _, n := range nrs {
		path := "/dev/video" + strconv.Itoa(n)
		names, err := sys.DeviceOpeners(path)
		if err != nil {
			return fmt.Errorf("cannot inspect holders of %s; not unloading v4l2loopback: %w", path, err)
		}
		for _, name := range names {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			held = append(held, name)
		}
	}
	if len(held) > 0 {
		return fmt.Errorf("cannot reload v4l2loopback while processes hold loopback devices: %s", strings.Join(held, ", "))
	}
	return nil
}

func containsInt(nrs []int, n int) bool {
	for _, v := range nrs {
		if v == n {
			return true
		}
	}
	return false
}

func invokingUser(sys System) string {
	if u := strings.TrimSpace(sys.Getenv("SUDO_USER")); u != "" && u != "root" {
		return u
	}
	if sys.Geteuid() == 0 {
		return ""
	}
	return strings.TrimSpace(sys.Getenv("USER"))
}

func groupExists(sys System, group string) bool {
	data, err := sys.ReadFile("/etc/group")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, _, ok := strings.Cut(line, ":")
		if ok && name == group {
			return true
		}
	}
	return false
}

func userInGroup(sys System, user, group string) bool {
	data, err := sys.ReadFile("/etc/group")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < 4 || parts[0] != group {
			continue
		}
		for _, member := range strings.Split(parts[3], ",") {
			if strings.TrimSpace(member) == user {
				return true
			}
		}
	}
	return false
}

func firewallPlan(sys System) [][]string {
	if sys.Run("systemctl", "is-active", "--quiet", "ufw") == nil {
		out, err := sys.Output("ufw", "status")
		if err == nil && doctor.UFWStatusAllowsPhoneCam(string(out)) {
			return nil
		}
		return [][]string{
			{"ufw", "allow", controlPortProto},
			{"ufw", "allow", rtpPortProto},
		}
	}
	if sys.Run("systemctl", "is-active", "--quiet", "firewalld") == nil {
		tcpOK := sys.Run("firewall-cmd", "--query-port="+controlPortProto) == nil
		udpOK := sys.Run("firewall-cmd", "--query-port="+rtpPortProto) == nil
		if tcpOK && udpOK {
			return nil
		}
		return [][]string{
			{"firewall-cmd", "--permanent", "--add-port=" + controlPortProto, "--add-port=" + rtpPortProto},
			{"firewall-cmd", "--reload"},
		}
	}
	return nil
}

// Lines is the human-readable dry-run / log form of the plan.
func (p Plan) Lines() []string {
	var lines []string
	lines = append(lines, "Distro: "+p.Family)
	if p.RPMFusion {
		lines = append(lines, "Enable RPM Fusion (v4l2loopback and gst-libav are not in stock Fedora)")
	}
	for _, cmd := range p.PreCmds {
		lines = append(lines, "Run: "+quoteCmd(cmd))
	}
	if len(p.PackageCmd) > 0 {
		lines = append(lines, "Install: "+quoteCmd(p.PackageCmd))
	}
	switch p.Loopback.Kind {
	case loopbackLeave:
		lines = append(lines, "Loopback: leave existing PhoneCam on /dev/video10 (exclusive_caps on)")
	case loopbackReloadOBS:
		if p.Loopback.Rmmod {
			lines = append(lines, "Loopback: unload v4l2loopback (no holders), then load OBS+PhoneCam")
		}
		lines = append(lines, "Load: modprobe v4l2loopback "+strings.Join(p.Loopback.Args, " "))
	case loopbackLoad, loopbackReload:
		if p.Loopback.Rmmod {
			lines = append(lines, "Loopback: unload v4l2loopback (no holders)")
		}
		lines = append(lines, "Load: modprobe v4l2loopback "+strings.Join(p.Loopback.Args, " "))
	}
	if p.WriteModprobe {
		lines = append(lines, "Write: "+ModprobeConfPath)
		lines = append(lines, "  "+strings.TrimSpace(p.ModprobeBody))
	}
	if p.WriteModulesLoad {
		lines = append(lines, "Write: "+ModulesLoadPath)
	}
	if p.GroupUser != "" {
		lines = append(lines, "Add user "+p.GroupUser+" to group "+videoGroup)
	}
	for _, cmd := range p.FirewallCmds {
		lines = append(lines, "Firewall: "+quoteCmd(cmd))
	}
	for _, w := range p.Warnings {
		lines = append(lines, "Warning: "+w)
	}
	return lines
}

func quoteCmd(cmd []string) string {
	return strings.Join(cmd, " ")
}

// Apply executes a previously built plan. It mutates the system.
func Apply(sys System, plan Plan, stdout io.Writer) error {
	for _, cmd := range plan.PreCmds {
		if err := sys.Run(cmd[0], cmd[1:]...); err != nil {
			if plan.RPMFusion {
				return errors.New(FedoraFusionMessage)
			}
			return fmt.Errorf("run %s: %w", quoteCmd(cmd), err)
		}
	}
	if len(plan.PackageCmd) > 0 {
		if err := sys.Run(plan.PackageCmd[0], plan.PackageCmd[1:]...); err != nil {
			if plan.RPMFusion {
				return errors.New(FedoraFusionMessage)
			}
			return fmt.Errorf("install packages: %w", err)
		}
		fmt.Fprintln(stdout, "Installed: "+strings.Join(plan.Packages, " "))
	}

	if plan.GroupUser != "" {
		if err := sys.Run("usermod", "-aG", videoGroup, plan.GroupUser); err != nil {
			return fmt.Errorf("add %s to group %s: %w", plan.GroupUser, videoGroup, err)
		}
		fmt.Fprintf(stdout, "Added %s to group %s; log out and back in before phonecam start.\n", plan.GroupUser, videoGroup)
	}

	if plan.Loopback.Rmmod {
		if err := sys.Run("rmmod", "v4l2loopback"); err != nil {
			return fmt.Errorf("rmmod v4l2loopback: %w", err)
		}
	}
	if plan.Loopback.Kind != loopbackLeave && len(plan.Loopback.Args) > 0 {
		args := append([]string{"v4l2loopback"}, plan.Loopback.Args...)
		if err := sys.Run("modprobe", args...); err != nil {
			return fmt.Errorf("modprobe v4l2loopback: %w", err)
		}
		fmt.Fprintln(stdout, "Loaded v4l2loopback "+strings.Join(plan.Loopback.Args, " "))
	} else {
		fmt.Fprintln(stdout, "Left existing PhoneCam loopback in place")
	}

	if plan.WriteModprobe {
		if err := writeConf(sys, ModprobeConfPath, plan.ModprobeBody); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Wrote "+ModprobeConfPath)
	}
	if plan.WriteModulesLoad {
		if err := writeConf(sys, ModulesLoadPath, plan.ModulesLoadBody); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Wrote "+ModulesLoadPath)
	}

	for _, cmd := range plan.FirewallCmds {
		if err := sys.Run(cmd[0], cmd[1:]...); err != nil {
			return fmt.Errorf("firewall %s: %w", quoteCmd(cmd), err)
		}
	}
	if len(plan.FirewallCmds) > 0 {
		fmt.Fprintln(stdout, "Opened "+controlPortProto+" and "+rtpPortProto)
	}

	for _, w := range plan.Warnings {
		fmt.Fprintln(stdout, "Warning: "+w)
	}
	fmt.Fprintln(stdout, "Setup complete. Run phonecam doctor, then phonecam start (not as root).")
	return nil
}

func writeConf(sys System, path, body string) error {
	if err := sys.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := sys.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// MutatingCommand reports whether a Run() invocation would change the system.
// Query helpers (systemctl is-active, firewall-cmd --query-port, ufw status)
// are not mutating.
func MutatingCommand(name string, args []string) bool {
	switch name {
	case "modprobe", "rmmod", "pacman", "dnf", "apt-get", "apt", "usermod", "gpasswd":
		return true
	case "ufw":
		return len(args) > 0 && args[0] != "status"
	case "firewall-cmd":
		for _, a := range args {
			if strings.HasPrefix(a, "--add-port") || a == "--reload" || a == "--permanent" {
				return true
			}
		}
	}
	return false
}
