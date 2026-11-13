package main

import (
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// droppedEnv 防止降权后重新 exec 时再次进入降权分支。
const droppedEnv = "VM_PRIV_DROPPED"

type runIdentity struct {
	UID   int
	GID   int
	Umask int
}

func parseIntEnv(name string, base int, def int) int {
	s := strings.TrimSpace(os.Getenv(name))
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, base, 32)
	if err != nil || v < 0 {
		log.Printf("警告：环境变量 %s=%q 无效，使用默认值 %d", name, s, def)
		return def
	}
	return int(v)
}

// firstIntEnv 取第一个被显式设置的环境变量；PUID/PGID 优先，兼容绿联面板常用的 UID/GID。
func firstIntEnv(names []string, base int, def int) int {
	for _, n := range names {
		if strings.TrimSpace(os.Getenv(n)) != "" {
			return parseIntEnv(n, base, def)
		}
	}
	return def
}

func readIdentity() runIdentity {
	return runIdentity{
		UID:   firstIntEnv([]string{"PUID", "UID"}, 10, 0),
		GID:   firstIntEnv([]string{"PGID", "GID"}, 10, 0),
		Umask: parseIntEnv("UMASK", 8, 022),
	}
}

func chownTree(root string, uid, gid int) error {
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

// applyIdentity 必须在 NewStore 之前调用。
// 镜像默认以 root 启动（兼容任意 NAS 共享目录）；若显式指定 PUID/PGID，
// 则先把数据目录属主改过去，再以目标身份 re-exec 自身并等待子进程。
// re-exec 是必需的：Go 多线程下直接 syscall.Setuid 只作用于调用线程，不可靠。
func applyIdentity(dataDir string) {
	id := readIdentity()
	syscall.Umask(id.Umask)

	euid, egid := os.Geteuid(), os.Getegid()
	if os.Getenv(droppedEnv) == "1" {
		log.Printf("运行身份：uid=%d gid=%d umask=%04o（已按 PUID/PGID 降权）", euid, egid, id.Umask)
		return
	}
	if euid != 0 {
		log.Printf("运行身份：uid=%d gid=%d umask=%04o（进程非 root，跳过降权）", euid, egid, id.Umask)
		return
	}
	if id.UID == 0 && id.GID == 0 {
		log.Printf("运行身份：root umask=%04o（未指定 PUID/PGID，保持 root 以兼容 NAS 共享目录）", id.Umask)
		return
	}

	uid, gid := id.UID, id.GID
	if gid == 0 {
		gid = uid
	}
	if err := chownTree(dataDir, uid, gid); err != nil {
		log.Printf("警告：无法将 %s 属主改为 %d:%d（%v），保持 root 运行", dataDir, uid, gid, err)
		return
	}
	log.Printf("数据目录 %s 属主已改为 %d:%d，准备降权启动", dataDir, uid, gid)

	self, err := os.Executable()
	if err != nil {
		log.Printf("警告：无法定位可执行文件（%v），保持 root 运行", err)
		return
	}
	attr := &os.ProcAttr{
		Env:   append(os.Environ(), droppedEnv+"=1"),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys: &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: uint32(uid), Gid: uint32(gid),
				Groups: []uint32{uint32(gid)},
			},
		},
	}
	proc, err := os.StartProcess(self, os.Args, attr)
	if err != nil {
		log.Printf("警告：降权启动失败（%v），保持 root 运行", err)
		return
	}

	// 父进程是容器 PID 1，负责把停止信号转发给真正的服务进程。
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for s := range sigs {
			_ = proc.Signal(s)
		}
	}()
	state, err := proc.Wait()
	if err != nil {
		log.Printf("服务进程异常结束：%v", err)
		os.Exit(1)
	}
	os.Exit(state.ExitCode())
}
