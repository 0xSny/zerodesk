# ZeroDesk N1-WOL (远程开机模块说明)

用户已确认 N1 盒子远程开机模块已有现成实现，Stage 3 不再重复开发此模块。

## Windows 主机端网卡唤醒核验记录（只读）
- 主物理网卡：`网线` (`Realtek Gaming 2.5GbE Family Controller`)
- 物理 MAC 地址：`E8:9C:25:9A:80:A0`（已自动写入 `data\config.json` 与 UDP `9528` 发现应答 `mac_address` 字段）
- 网卡高级电源属性（只读核验）：
  - 魔术封包唤醒：开启
  - 样式比对唤醒：开启
  - 关机 网络唤醒：开启
