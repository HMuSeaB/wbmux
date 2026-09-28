// Package update 界面内的自助升级：查最新 release、下载、原位替换。
//
// # 为什么不做成安装器
//
// wbmux 是单文件绿色程序，"安装"本来就是把它放进任意目录，安装目录零改动
// （见 README）。做一个 NSIS/Inno 安装器反而与卖点打架。所谓升级 = 用新版
// exe 覆盖旧 exe——本包把这件事做成界面里的两次点击：检查 → 一键升级。
//
// # 自替换为什么可行
//
// Windows 允许给**正在运行**的 exe 改名，但不允许覆盖/删除它。所以顺序是：
// 把运行中的 exe 原地改名成 .old → 在腾出来的原路径写入新 exe → 下次启动
// 即为新版。.old 在下次启动时清理（CleanupOld，由 cmd_gui 调用）。
//
// # 网络现实
//
// 更新源是 GitHub Releases。这台机器访问 GitHub 时通时断（git 推送都有
// 时通时断的实测记录），所以 Check/Apply 的失败路径必须把 release 页面
// URL 带出来，让用户能手动下载兜底。
package update

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
)

const repo = "HMuSeaB/wbmux"
const latestAPI = "https://api.github.com/repos/" + repo + "/releases/latest"
const releasePage = "https://github.com/" + repo + "/releases/latest"

// httpTimeout 覆盖检查（快）与下载（慢，几 MB 走 GitHub 时通时断的链路），
// 取宽不取窄：超时宁可多等，也别在 90% 进度时砍掉重来。
const httpTimeout = 120 * time.Second

// ghAsset 是 GitHub release 资产里本包关心的两个字段。
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Info 是一次更新检查的结果。
type Info struct {
	Current  string `json:"current"`  // 当前运行的版本（构建期注入）
	Latest   string `json:"latest"`   // 最新已发布版本的 tag（带 v 前缀）
	URL      string `json:"url"`      // release 页面，手动下载兜底用
	AssetURL string `json:"assetURL"` // Windows amd64 zip 的直链；非 Windows 平台为空
	UpToDate bool   `json:"upToDate"`
}

// Check 向 GitHub 查最新已发布版本，与当前版本比较。
//
// 草稿 release 不算"已发布"（GitHub 的 /releases/latest 本来就跳过草稿），
// 与 goreleaser draft:true 的人工确认流程正好咬合：确认发布后用户才看得到。
func Check(current string) (Info, error) {
	info := Info{Current: displayVersion(current), URL: releasePage}

	req, err := http.NewRequest(http.MethodGet, latestAPI, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("访问 GitHub 失败（该链路时通时断，可打开 %s 手动下载）：%w", releasePage, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("GitHub 返回 %s", resp.Status)
	}

	var rel struct {
		TagName string    `json:"tag_name"`
		HTMLURL string    `json:"html_url"`
		Assets  []ghAsset `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return info, err
	}
	if rel.TagName == "" {
		return info, errors.New("GitHub 没有已发布的 release")
	}

	info.Latest = rel.TagName
	if rel.HTMLURL != "" {
		info.URL = rel.HTMLURL
	}
	if runtime.GOOS == "windows" {
		info.AssetURL = pickWindowsAsset(rel.Assets)
	}
	info.UpToDate = !isNewer(rel.TagName, current)
	return info, nil
}

// isNewer 判断 latest tag 是否比当前版本新。
//
// 版本来自两处：goreleaser 注入的 {{.Version}}（tag 去掉 v 前缀，如 "0.5.0"）
// 与 GitHub 的 tag_name（带 v，如 "v0.5.0"）。比较前把两边的 v 都剥掉。
//
// 当前版本解析不出来（开发版 "dev"、异常构建）时**要**提示更新——dev 用户
// 手里没有正式版，有正式发布就该看见；解析不出来的反而是 latest tag，
// 那种情况不乱推荐。
func isNewer(latestTag, current string) bool {
	l, lok := semverParts(latestTag)
	if !lok {
		return false
	}
	c, cok := semverParts(current)
	if !cok {
		return true
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	// 数字位全等时按 semver 语义收尾：预发布（-beta1/-rc 之类）比同号
	// 正式发布老——跑 beta 的用户该被提示升级正式版；反之不推 beta。
	latestPre := strings.Contains(strings.TrimPrefix(strings.TrimSpace(latestTag), "v"), "-")
	currentPre := strings.Contains(strings.TrimPrefix(strings.TrimSpace(current), "v"), "-")
	return !latestPre && currentPre
}

// semverParts 解析出 major/minor/patch；解析失败 ok 为 false。
func semverParts(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		// 容忍 "0.5.0-beta1" 这类尾巴：数字段后跟非数字就截断。
		num := ""
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			num += string(r)
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// pickWindowsAsset 从 release 资产里挑 Windows amd64 的 zip。
// goreleaser 的命名模板是 wbmux_<版本>_<os>_<arch>.zip，直接认后缀段。
func pickWindowsAsset(assets []ghAsset) string {
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, "windows_amd64.zip") {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// Apply 下载并原位替换当前运行的 exe。
// 返回给界面的话要说清"替换已就位、重启才生效"——正在运行的还是旧版。
func Apply(assetURL string) (string, error) {
	if assetURL == "" {
		return "", errors.New("没有可用的 Windows 安装包（该 release 可能没有 windows_amd64 构建产物）")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", err
	}

	// 下载到 wbmux 自己的 tmp（与 migrate 的桥脚本同一个目录，出问题
	// 用户能自己看到，也便于整目录清理）。
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(tmp, "wbmux-update.zip")
	newExe := filepath.Join(tmp, "wbmux-new.exe")
	defer os.Remove(zipPath)
	defer os.Remove(newExe)

	if err := download(assetURL, zipPath); err != nil {
		return "", err
	}
	if err := extractExe(zipPath, newExe); err != nil {
		return "", err
	}

	// 原地替换：改名正在运行的 exe（Windows 允许），再在原路径放新版。
	// 失败就把改名撤回来，绝不让用户落到"新旧都没有"的地步。
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return "", fmt.Errorf("无法挪动正在运行的 wbmux.exe（可能被杀毒软件占用）：%w", err)
	}
	if err := copyFile(newExe, exe); err != nil {
		if rbErr := os.Rename(old, exe); rbErr != nil {
			return "", fmt.Errorf("写入新版失败，且旧版回滚也失败（旧版被改名为 %s，请手动改回）：%v；%v", old, err, rbErr)
		}
		return "", fmt.Errorf("写入新版失败（已回滚）：%w", err)
	}
	return "新版已就位。从托盘退出 wbmux，再启动一次即完成升级。", nil
}

// CleanupOld 清掉上次升级留下的 .old。由 cmd_gui 在启动早期调用，
// 失败静默——残留文件不挡路，下次启动还会再试。
func CleanupOld() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(exe + ".old")
}

// download 把 url 拉到 path（覆盖写）。
func download(url, path string) error {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败（该链路时通时断，可打开 %s 手动下载）：%w", releasePage, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：GitHub 返回 %s", resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractExe 从 goreleaser 的 zip 里解出 wbmux.exe 并做最低限度的完整性
// 检查（PE 头 + 体量）：下载链路时通时断，宁可多疑，也别把半个 exe 换上去。
func extractExe(zipPath, outPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("安装包损坏（可能是下载中断）：%w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !strings.EqualFold(f.Name, "wbmux.exe") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(outPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return checkPE(outPath)
	}
	return errors.New("安装包里没有 wbmux.exe")
}

// checkPE 校验文件是 Windows PE 可执行（MZ 头 + 体量下限）。
func checkPE(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil {
		return err
	}
	if head[0] != 'M' || head[1] != 'Z' {
		return errors.New("解出的文件不是 Windows 可执行文件")
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() < 4<<20 { // wbmux.exe 约 6.3MB，半个包都到不了这个数
		return errors.New("解出的文件不完整（体量异常）")
	}
	return nil
}

// copyFile 覆盖写。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// displayVersion 给界面展示的当前版本：空值一律显示成 dev，
// 与 version.String() 的零值口径一致。
func displayVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "dev"
	}
	return v
}
