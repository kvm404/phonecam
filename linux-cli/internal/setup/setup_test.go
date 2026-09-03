package setup

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kvm404/phonecam/linux-cli/internal/v4l2"
)

type fakeSys struct {
	euid     int
	env      map[string]string
	uname    string
	files    map[string]string
	exists   map[string]bool
	failures map[string]error
	outputs  map[string]string
	openers  map[string][]string
	openErr  map[string]error
	afterRun func(name string, args []string)

	ran    [][]string
	writes map[string]string
	mkdirs []string
}

func (f *fakeSys) Geteuid() int { return f.euid }

func (f *fakeSys) Getenv(name string) string { return f.env[name] }

func (f *fakeSys) UnameRelease() (string, error) { return f.uname, nil }

func (f *fakeSys) ReadFile(path string) ([]byte, error) {
	if content, ok := f.files[path]; ok {
		return []byte(content), nil
	}
	return nil, errors.New("not found")
}

func (f *fakeSys) WriteFile(path string, data []byte, perm os.FileMode) error {
	if f.writes == nil {
		f.writes = map[string]string{}
	}
	f.writes[path] = string(data)
	return nil
}

func (f *fakeSys) MkdirAll(path string, perm os.FileMode) error {
	f.mkdirs = append(f.mkdirs, path)
	return nil
}

func (f *fakeSys) Exists(path string) bool { return f.exists[path] }

func (f *fakeSys) Run(name string, args ...string) error {
	f.ran = append(f.ran, append([]string{name}, args...))
	key := strings.Join(append([]string{name}, args...), " ")
	if f.afterRun != nil {
		f.afterRun(name, args)
	}
	if f.failures != nil {
		if err, ok := f.failures[key]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeSys) Output(name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	if f.failures != nil {
		if err, ok := f.failures[key]; ok {
			return []byte(f.outputs[key]), err
		}
	}
	if f.outputs != nil {
		if out, ok := f.outputs[key]; ok {
			return []byte(out), nil
		}
	}
	return nil, errors.New("no output")
}

func (f *fakeSys) DeviceOpeners(path string) ([]string, error) {
	if f.openErr != nil {
		if err, ok := f.openErr[path]; ok {
			return nil, err
		}
	}
	return f.openers[path], nil
}

func archBase() *fakeSys {
	return &fakeSys{
		euid:  1000,
		uname: "6.10.1-arch1-1",
		env:   map[string]string{"USER": "alice"},
		files: map[string]string{
			"/etc/os-release": "ID=arch\n",
			"/etc/group":      "video:x:44:\n",
			"/usr/lib/modules/6.10.1-arch1-1/pkgbase": "linux\n",
		},
		exists: map[string]bool{},
		failures: map[string]error{
			"systemctl is-active --quiet ufw":       errors.New("inactive"),
			"systemctl is-active --quiet firewalld": errors.New("inactive"),
		},
	}
}

func TestBuildPlanArchPackagesMatchKernelHeaders(t *testing.T) {
	sys := archBase()
	sys.files["/usr/lib/modules/6.10.1-arch1-1/pkgbase"] = "linux-zen\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Family != "arch" {
		t.Fatalf("family=%s", plan.Family)
	}
	joined := strings.Join(plan.Packages, " ")
	if !strings.Contains(joined, "v4l2loopback-dkms") {
		t.Fatalf("expected v4l2loopback-dkms, got %v", plan.Packages)
	}
	if !strings.Contains(joined, "linux-zen-headers") {
		t.Fatalf("expected headers matching running kernel, got %v", plan.Packages)
	}
	if strings.Contains(joined, "gst-plugins-ugly") {
		t.Fatal("gst-plugins-ugly must not be a hard setup package")
	}
	if !strings.Contains(strings.Join(plan.PackageCmd, " "), "pacman") {
		t.Fatalf("expected pacman, got %v", plan.PackageCmd)
	}
}

func TestBuildPlanDebianHeadersAndNoUgly(t *testing.T) {
	sys := archBase()
	sys.uname = "6.8.0-40-generic"
	sys.files["/etc/os-release"] = "ID=ubuntu\nID_LIKE=debian\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Family != "debian" {
		t.Fatalf("family=%s", plan.Family)
	}
	joined := strings.Join(plan.Packages, " ")
	if !strings.Contains(joined, "v4l2loopback-dkms") || !strings.Contains(joined, "linux-headers-6.8.0-40-generic") {
		t.Fatalf("expected dkms + uname headers, got %v", plan.Packages)
	}
	if strings.Contains(joined, "gst-plugins-ugly") || strings.Contains(joined, "gstreamer1.0-plugins-ugly") {
		t.Fatal("gst-plugins-ugly must not be a hard setup package")
	}
	if plan.PreCmds[0][0] != "apt-get" {
		t.Fatalf("expected apt-get update, got %v", plan.PreCmds)
	}
}

func TestBuildPlanFedoraEnablesRPMFusion(t *testing.T) {
	sys := archBase()
	sys.files["/etc/os-release"] = "ID=fedora\nVERSION_ID=41\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RPMFusion {
		t.Fatal("expected RPM Fusion enable when repo is missing")
	}
	if len(plan.PreCmds) == 0 || !strings.Contains(strings.Join(plan.PreCmds[0], " "), "rpmfusion-free-release-41") {
		t.Fatalf("expected RPM Fusion dnf install, got %v", plan.PreCmds)
	}
	if strings.Contains(strings.Join(plan.Packages, " "), "gst-plugins-ugly") {
		t.Fatal("gst-plugins-ugly must not be a hard setup package")
	}
}

func TestBuildPlanFedoraFailsWithoutVersionWhenFusionMissing(t *testing.T) {
	sys := archBase()
	sys.files["/etc/os-release"] = "ID=fedora\n"
	_, err := BuildPlan(sys)
	if err == nil || !strings.Contains(err.Error(), "not in stock Fedora") {
		t.Fatalf("expected Fedora Fusion doctor line, got %v", err)
	}
}

func TestBuildPlanUnknownDistro(t *testing.T) {
	sys := archBase()
	sys.files["/etc/os-release"] = "ID=nixos\n"
	_, err := BuildPlan(sys)
	if err == nil || !strings.Contains(err.Error(), "unsupported distro") {
		t.Fatalf("expected unsupported distro, got %v", err)
	}
}

func TestLoopbackUnloadedLoadsPhoneCam(t *testing.T) {
	sys := archBase()
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Loopback.Kind != loopbackLoad || plan.Loopback.Rmmod {
		t.Fatalf("loopback=%+v", plan.Loopback)
	}
	got := strings.Join(plan.Loopback.Args, " ")
	if got != "video_nr=10 card_label=PhoneCam exclusive_caps=1" {
		t.Fatalf("args=%q", got)
	}
	if !plan.WriteModprobe || !strings.Contains(plan.ModprobeBody, "exclusive_caps=1") {
		t.Fatalf("persist=%q write=%v", plan.ModprobeBody, plan.WriteModprobe)
	}
	if !plan.WriteModulesLoad {
		t.Fatal("expected modules-load.d/phonecam.conf")
	}
}

func TestLoopbackLeavePhoneCamExclusiveCapsOn(t *testing.T) {
	sys := archBase()
	sys.exists["/sys/module/v4l2loopback"] = true
	sys.exists["/dev/video10"] = true
	sys.files["/sys/class/video4linux/video10/name"] = "PhoneCam\n"
	sys.files[v4l2.VideoNrParameterPath] = "10,-1,-1,-1\n"
	sys.files[v4l2.ExclusiveCapsParameterPath] = "Y,N,N,N\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Loopback.Kind != loopbackLeave {
		t.Fatalf("expected leave, got %+v", plan.Loopback)
	}
	if plan.Loopback.Rmmod {
		t.Fatal("leave must not rmmod")
	}
}

func TestLoopbackOBSMergeWithoutPhoneCam(t *testing.T) {
	sys := archBase()
	sys.exists["/sys/module/v4l2loopback"] = true
	sys.exists["/dev/video0"] = true
	sys.files["/sys/class/video4linux/video0/name"] = "OBS Virtual Camera\n"
	sys.files[v4l2.VideoNrParameterPath] = "0,-1,-1,-1\n"
	sys.files[v4l2.ExclusiveCapsParameterPath] = "Y,N,N,N\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Loopback.Kind != loopbackReloadOBS || !plan.Loopback.Rmmod {
		t.Fatalf("expected OBS reload, got %+v", plan.Loopback)
	}
	got := strings.Join(plan.Loopback.Args, " ")
	if !strings.Contains(got, "devices=2") || !strings.Contains(got, "video_nr=0,10") || !strings.Contains(got, "exclusive_caps=1,1") {
		t.Fatalf("args=%q", got)
	}
	if strings.Count(got, "exclusive_caps=1,1") != 1 {
		t.Fatalf("exclusive_caps must be per-device CSV 1,1, got %q", got)
	}
}

func TestLoopbackBareModprobeReloadsPhoneCam(t *testing.T) {
	sys := archBase()
	sys.exists["/sys/module/v4l2loopback"] = true
	sys.files[v4l2.VideoNrParameterPath] = "-1,-1,-1,-1\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Loopback.Rmmod {
		t.Fatalf("loaded default module must rmmod before video_nr=10, got %+v", plan.Loopback)
	}
	got := strings.Join(plan.Loopback.Args, " ")
	if got != "video_nr=10 card_label=PhoneCam exclusive_caps=1" {
		t.Fatalf("args=%q", got)
	}
}

func TestLoopbackAutoAssignedVideo0MergesOBS(t *testing.T) {
	sys := archBase()
	sys.exists["/sys/module/v4l2loopback"] = true
	sys.exists["/dev/video0"] = true
	sys.exists["/sys/devices/virtual/video4linux/video0"] = true
	sys.files[v4l2.VideoNrParameterPath] = "-1,-1,-1,-1\n"
	sys.files["/sys/class/video4linux/video0/name"] = "OBS Virtual Camera\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Loopback.Kind != loopbackReloadOBS || !plan.Loopback.Rmmod {
		t.Fatalf("expected OBS merge for auto-assigned video0, got %+v", plan.Loopback)
	}
	got := strings.Join(plan.Loopback.Args, " ")
	if !strings.Contains(got, "devices=2") || !strings.Contains(got, "video_nr=0,10") || !strings.Contains(got, "exclusive_caps=1,1") {
		t.Fatalf("args=%q", got)
	}
}

func TestLoopbackOBSMergeRefusesHolders(t *testing.T) {
	sys := archBase()
	sys.exists["/sys/module/v4l2loopback"] = true
	sys.files[v4l2.VideoNrParameterPath] = "0,-1,-1,-1\n"
	sys.openers = map[string][]string{"/dev/video0": {"obs"}}
	_, err := BuildPlan(sys)
	if err == nil || !strings.Contains(err.Error(), "obs") {
		t.Fatalf("expected holders error, got %v", err)
	}
}

func TestLoopbackDoesNotStealNonPhoneCamVideo10(t *testing.T) {
	sys := archBase()
	sys.exists["/dev/video10"] = true
	sys.exists["/sys/module/v4l2loopback"] = true
	sys.files["/sys/class/video4linux/video10/name"] = "Integrated Camera\n"
	_, err := BuildPlan(sys)
	if err == nil || !strings.Contains(err.Error(), "not stealing") {
		t.Fatalf("expected steal refusal, got %v", err)
	}
}

func TestOBSOwnedModprobeConfIsNeverOverwritten(t *testing.T) {
	sys := archBase()
	sys.exists[OBSModprobeConfPath] = true
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.WriteModprobe {
		t.Fatal("must not write phonecam.conf alongside OBS-owned v4l2loopback.conf")
	}
	if !plan.WriteModulesLoad {
		t.Fatal("still persist modules-load.d/phonecam.conf")
	}

	sys.euid = 0
	var out bytes.Buffer
	if err := Apply(sys, plan, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := sys.writes[OBSModprobeConfPath]; ok {
		t.Fatal("overwrote OBS-owned v4l2loopback.conf")
	}
	if _, ok := sys.writes[ModprobeConfPath]; ok {
		t.Fatal("wrote phonecam.conf despite OBS-owned conf")
	}
	if _, ok := sys.writes[ModulesLoadPath]; !ok {
		t.Fatal("expected modules-load.d/phonecam.conf")
	}
}

func TestGroupAddOnlyWhenMissing(t *testing.T) {
	sys := archBase()
	sys.env["SUDO_USER"] = "alice"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.GroupUser != "alice" {
		t.Fatalf("expected to add alice, got %q", plan.GroupUser)
	}

	sys.files["/etc/group"] = "video:x:44:alice\n"
	plan, err = BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.GroupUser != "" {
		t.Fatalf("already in video; got %q", plan.GroupUser)
	}
}

func TestFirewallOnlyWhenBlocking(t *testing.T) {
	sys := archBase()
	sys.failures = map[string]error{
		"systemctl is-active --quiet firewalld": errors.New("inactive"),
	}
	sys.outputs = map[string]string{"ufw status": "Status: active\n"}
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FirewallCmds) != 2 {
		t.Fatalf("expected ufw allow cmds, got %v", plan.FirewallCmds)
	}

	sys.outputs["ufw status"] = "Status: active\n47470/tcp ALLOW Anywhere\n47471/udp ALLOW Anywhere\n"
	plan, err = BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FirewallCmds) != 0 {
		t.Fatalf("ufw already allows; got %v", plan.FirewallCmds)
	}

	sys.outputs["ufw status"] = ""
	sys.failures["ufw status"] = errors.New("need root")
	sys.files["/etc/ufw/user.rules"] = `
-A ufw-user-input -p tcp --dport 47470 -j ACCEPT
-A ufw-user-input -p udp --dport 47471 -j ACCEPT
`
	plan, err = BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FirewallCmds) != 0 {
		t.Fatalf("ufw user.rules already allow; got %v", plan.FirewallCmds)
	}

	sys.failures = map[string]error{
		"systemctl is-active --quiet ufw": errors.New("inactive"),
	}
	plan, err = BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FirewallCmds) != 0 {
		t.Fatalf("firewalld query-port succeeds by default; got %v", plan.FirewallCmds)
	}

	sys.failures["firewall-cmd --query-port=47470/tcp"] = errors.New("no")
	sys.failures["firewall-cmd --query-port=47471/udp"] = errors.New("no")
	plan, err = BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FirewallCmds) == 0 {
		t.Fatal("expected firewalld add-port when query-port fails")
	}
}

func TestDryRunPrintsPlanAndDoesNotMutate(t *testing.T) {
	sys := archBase()
	var stdout, stderr bytes.Buffer
	if err := Run(sys, Config{DryRun: true}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "No changes were made") {
		t.Fatalf("got:\n%s", out)
	}
	if !strings.Contains(out, "video_nr=10 card_label=PhoneCam exclusive_caps=1") {
		t.Fatalf("expected load plan, got:\n%s", out)
	}
	if len(sys.writes) != 0 || len(sys.mkdirs) != 0 {
		t.Fatalf("dry-run wrote files: %v mkdirs=%v", sys.writes, sys.mkdirs)
	}
	for _, cmd := range sys.ran {
		if MutatingCommand(cmd[0], cmd[1:]) {
			t.Fatalf("dry-run ran mutating command %v", cmd)
		}
	}
}

func TestRunRefusesNonRootWithoutDryRun(t *testing.T) {
	sys := archBase()
	sys.euid = 1000
	err := Run(sys, Config{}, ioDiscard(), ioDiscard())
	if !errors.Is(err, ErrNeedRoot) {
		t.Fatalf("got %v", err)
	}
	if len(sys.writes) != 0 {
		t.Fatal("non-root setup must not write")
	}
}

func ioDiscard() *bytes.Buffer { return &bytes.Buffer{} }

func TestApplyDoesNotWriteOBSConf(t *testing.T) {
	sys := archBase()
	sys.euid = 0
	sys.env["SUDO_USER"] = "alice"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Apply(sys, plan, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := sys.writes[OBSModprobeConfPath]; ok {
		t.Fatal("must never write v4l2loopback.conf")
	}
	if !strings.Contains(sys.writes[ModprobeConfPath], "card_label=PhoneCam") {
		t.Fatalf("modprobe conf=%q", sys.writes[ModprobeConfPath])
	}
	if strings.TrimSpace(sys.writes[ModulesLoadPath]) != "v4l2loopback" {
		t.Fatalf("modules-load=%q", sys.writes[ModulesLoadPath])
	}
	var sawModprobe, sawPacman, sawUsermod bool
	for _, cmd := range sys.ran {
		switch cmd[0] {
		case "modprobe":
			sawModprobe = true
			if strings.Join(cmd[1:], " ") != "v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1" {
				t.Fatalf("modprobe %v", cmd)
			}
		case "pacman":
			sawPacman = true
		case "usermod":
			sawUsermod = true
		}
	}
	if !sawModprobe || !sawPacman || !sawUsermod {
		t.Fatalf("missing apply steps: modprobe=%v pacman=%v usermod=%v ran=%v", sawModprobe, sawPacman, sawUsermod, sys.ran)
	}
}

func TestApplyReloadsAfterPackageAutoLoad(t *testing.T) {
	sys := archBase()
	sys.euid = 0
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Loopback.Rmmod {
		t.Fatal("unloaded module should plan a first load")
	}
	sys.afterRun = func(name string, args []string) {
		if name == "pacman" {
			sys.exists["/sys/module/v4l2loopback"] = true
			sys.files[v4l2.VideoNrParameterPath] = "-1,-1,-1,-1\n"
		}
	}
	var out bytes.Buffer
	if err := Apply(sys, plan, &out); err != nil {
		t.Fatal(err)
	}
	var sawRmmod, sawModprobe bool
	for _, cmd := range sys.ran {
		switch cmd[0] {
		case "rmmod":
			sawRmmod = true
		case "modprobe":
			sawModprobe = true
			if strings.Join(cmd[1:], " ") != "v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1" {
				t.Fatalf("modprobe %v", cmd)
			}
		}
	}
	if !sawRmmod || !sawModprobe {
		t.Fatalf("package auto-load must rmmod then load PhoneCam, ran=%v", sys.ran)
	}
}

func TestApplyFedoraFusionFailureUsesDoctorLine(t *testing.T) {
	sys := archBase()
	sys.euid = 0
	sys.files["/etc/os-release"] = "ID=fedora\nVERSION_ID=41\n"
	plan, err := BuildPlan(sys)
	if err != nil {
		t.Fatal(err)
	}
	sys.failures[strings.Join(plan.PreCmds[0], " ")] = errors.New("dnf failed")
	err = Apply(sys, plan, ioDiscard())
	if err == nil || err.Error() != FedoraFusionMessage {
		t.Fatalf("got %v", err)
	}
}
