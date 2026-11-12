# 浏览器日志编码表

这些二进制表及浏览器模块由 [生成器](../../../scripts/generate-log-encoding.mjs) 从 WHATWG Encoding 固定提交 `be17ac5c49881336538acfc7b40dba3c8009144b` 生成。来源为该提交下的 `index-gb18030.txt`、`index-gb18030-ranges.txt`、`index-big5.txt`；下载地址前缀为 `https://raw.githubusercontent.com/whatwg/encoding/be17ac5c49881336538acfc7b40dba3c8009144b/`。输入 SHA-256 固定在生成器中，任何变化都会拒绝生成。

完整许可见 [LICENSE](LICENSE)。嵌入源码的表和算法适用其中 BSD 3-Clause 条款，版权属于 WHATWG (Apple, Google, Mozilla, Microsoft)。此处仅改变表的存储格式：索引表为按指针排列的 little-endian uint32，零表示未定义；范围表为 little-endian uint32 的指针、码点对。生成文件按第三方数据处理，不适用手写代码行数建议。

维护者将上述三个表和 LICENSE 下载到同一临时目录后运行 `node scripts/generate-log-encoding.mjs <source-directory>`；加 `--check` 验证已保存的数据。不会在产品启动或测试时访问网络。Go/浏览器随机字节与跨帧测试见 [log_text_test.go](../log_text_test.go) 和 [log_encoding_test.go](../log_encoding_test.go)。
