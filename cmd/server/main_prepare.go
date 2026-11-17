// main_prepare.go 启动期配置准备：加载 / 首次运行自动生成 / 暴露安全闸。
// 从 main() 切出（2026-11-17）：这段逻辑只做「加载 + 兜底 + 校验」，抽成可返回
// error 的函数后 main 只剩一条 if。
package main

import (
	"fmt"
	"log"
)

// prepareConfig 加载配置；首次运行（文件确实缺失）自动落一份推荐配置再加载，
// 生成失败退回「纯默认 + env」不阻塞启动。启动期安全闸（无密钥 + 非回环）在此拒绝。
//
// 逐条语义与拆分前 main() 内联版本一致：
//   - errors.Is 才能看穿 Load 里 fmt.Errorf("%w") 的包装；os.IsNotExist 不行；
//   - 必须验证 config 文件本身缺失，不能只看错误类型——prompt.file 等附属文件缺失
//     同样报 ErrNotExist，误判会丢弃正常配置（2026-10-23 实弹）；
//   - 不回显 key：日志会被重定向/采集/贴进 issue，明文 api_key 落日志等于凭证
//     外泄（R1/R2）。key 已在配置文件里，提示路径即可。
func prepareConfig(cfgPath string) (*Config, error) {
	cfg, err := Load(cfgPath)
	if err != nil {
		if configFileGenuinelyMissing(cfgPath, err) {
			// 首次运行：目录下没有配置 → 自动落一份推荐配置（含随机 api_key）再加载。
			// 双击 exe / 裸跑 docker 即开，无需先手工复制样例。
			if _, werr := WriteDefault(cfgPath); werr == nil {
				log.Printf("%s", firstRunNotice(cfgPath))
				cfg, err = Load(cfgPath)
			}
			if err != nil {
				// 生成失败（目录只读等）：退回纯默认 + env（旧行为兜底），不阻塞启动。
				log.Printf("config %s not found (auto-generate failed), using defaults+env: %v", cfgPath, err)
				cfg, err = Load("")
			}
		}
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
	}
	// 启动期安全闸：没有生效密钥 + 监听非回环 = 网关与面板裸暴露给同网段，拒绝启动。
	// 判据见 config_exposure.go（单测 config_exposure_test.go），不再靠注释提醒。
	if err := CheckUnauthenticatedExposure(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
