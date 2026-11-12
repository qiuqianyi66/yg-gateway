package main

// saveConfig 热应用的字段变化门控：B 档 poke 只在相关字段真的变化时执行，
// 保存无关字段不重算排程/余额。

// scheduleReconfigureNeeded 判断排程 Reconfigure 的入参（6 组小时窗 + 6 个启用开关）
// 是否有任一字段变化；全部相等时热应用无需 poke 排程/余额重算。
func scheduleReconfigureNeeded(oldCfg, newCfg *Config) bool {
	o, n := oldCfg.Schedule, newCfg.Schedule
	hourPairs := [][2][]int{
		{o.CheckinHours, n.CheckinHours}, {o.TravelHours, n.TravelHours},
		{o.ActivityHours, n.ActivityHours}, {o.KeepaliveHours, n.KeepaliveHours},
		{o.BlackcatHours, n.BlackcatHours}, {o.GrowthHours, n.GrowthHours},
	}
	for _, pair := range hourPairs {
		if len(pair[0]) != len(pair[1]) {
			return true
		}
		for i := range pair[0] {
			if pair[0][i] != pair[1][i] {
				return true
			}
		}
	}
	enabledPairs := [2][6]bool{
		{o.CheckinEnabled, o.TravelEnabled, o.ActivityEnabled, o.KeepaliveEnabled, o.BlackcatEnabled, o.GrowthEnabled},
		{n.CheckinEnabled, n.TravelEnabled, n.ActivityEnabled, n.KeepaliveEnabled, n.BlackcatEnabled, n.GrowthEnabled},
	}
	for i := range enabledPairs[0] {
		if enabledPairs[0][i] != enabledPairs[1][i] {
			return true
		}
	}
	return false
}
