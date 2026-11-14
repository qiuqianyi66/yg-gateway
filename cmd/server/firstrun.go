package main

import "fmt"

// firstRunNotice 构造「首次运行已生成推荐配置」的日志文案。
//
// 刻意不回显 api_key：日志常被重定向到文件、被采集系统收走、或被整段贴进工单，
// 明文密钥落日志即凭证外泄（R1/R2）。key 已在配置文件里，提示路径即可。
//
// 单独成文件而非塞进 main.go：main.go 已 1062 行，逼近 scripts/check-depth.ps1
// 的 1000 行阈值与棘轮基线（只许收紧），新逻辑一律落新文件。
func firstRunNotice(path string) string {
	return fmt.Sprintf("config %s 不存在，已生成推荐配置（api_key 已写入该文件，请打开 %s 查看或自行修改）", path, path)
}
