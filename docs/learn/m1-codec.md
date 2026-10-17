# M1 学习笔记：协议内核为什么这么写

读代码的顺序建议：`ws/frame.go` → `ws/decoder.go` → `ws/mask.go` → `ws/violation.go` →
`ws/utf8.go` → `ws/assembler.go` → `sse/parser.go`，每读一个文件就跑对应的测试。

## 0. 总原则：三个问题分开回答

WebSocket 的一个字节流要经过三个互不相同的问题，普通库把它们揉在一起，调试器必须拆开：

| 问题 | 代码 | 为什么要独立 |
| --- | --- | --- |
| 这些字节能不能切成帧？ | `Decoder` / `AppendFrame` | 只做语法，不下判断，所以非法帧也能被记录、转发、原样重放 |
| 这个帧合不合规？ | `Header.Violations` | 返回**全部**违规而不是第一个，界面才能一次列清 |
| 帧能不能拼成消息？ | `Assembler` | 严格层：分片、控制帧穿插、UTF-8、大小，出错时给出该发的关闭码 |

知识点：**解析（parse）与校验（validate）分离**，是做抓包、代理、调试类工具的基本功。

## 1. `frame.go`：帧头与三种长度形式

RFC 6455 §5.2。第二个字节低 7 位是长度：0–125 直接放；126 表示后面跟 2 字节；127 表示后面跟 8 字节（最高位必须为 0）。

- 边界是 **125 / 126 / 65535 / 65536**，也是最容易写出 off-by-one 的地方，见 `TestLengthBoundaries`。
- RFC 要求用最短形式。可是 `Header` 里存了 `LenEncoding`（实际用的形式），这样“用 16 位形式发 3 字节”这种非法帧也能被**字节级还原**，同时 `Violations` 会报 `non_minimal_length`。
- `Opcode` 保留未知值（3–7、0xB–0xF），不丢信息。`IsControl()` 的判断是“最高位为 1”，所以保留的控制码也算控制帧。
- 代码里的惯用法：`AppendFrame(dst, ...)` 采用 `append` 风格，调用方控制缓冲区，避免每帧分配（和标准库 `strconv.AppendInt` 同一思路）。

## 2. `decoder.go`：对不可信输入做的四件事

1. **先查上限，再分配内存。** 长度字段由对端控制，`PayloadLen > max` 在读 payload 之前就拒绝（`ErrFrameTooLarge`，并把帧头一起返回供界面显示）。
2. **按实际到达的字节增长。** 小于 64 KiB 一次分配；更大的用 `io.CopyN` 进 `bytes.Buffer`。否则对端声明 16 MiB 再只发 10 字节，就能逼我们白白占 16 MiB（`TestDecoderErrors/large announced payload`）。
3. **区分“干净结束”和“被截断”。** 正好在帧边界结束返回 `io.EOF`；帧中间结束返回 `io.ErrUnexpectedEOF`。`fill()` 里把 `ReadFull` 在帧内的 `EOF` 改写成 `ErrUnexpectedEOF` 就是为了这个。界面据此能说“对端正常结束”还是“连接被切断”。
4. **64 位长度最高位为 1 直接报错。** 这种长度不可能是合法的，也无法重新同步。

知识点：`io.ReadFull` / `io.CopyN` 的错误语义；永远不要相信网络数据里的长度。

## 3. `mask.go`：掩码不是加密

RFC 6455 §5.3：客户端到服务端的帧必须掩码，目的是防止恶意网页构造出能被**透明 HTTP 代理**误当成请求的字节（缓存投毒），不是为了保密；密钥就在帧里。XOR 的逆运算是它自己，所以同一个函数既掩码也解码。

- 第 i 个字节用 `key[i mod 4]`。流式处理时一块数据不是从 0 开始，所以有 `MaskAt(key, offset, b)`。
- 快路径一次异或 8 字节（`uint64`）。8 是 4 的倍数，所以每个 8 字节块开头的密钥相位都相同。
- 怎么证明快路径没写错：`TestMaskMatchesNaive` 拿朴素逐字节实现当**参照物（oracle）**，对随机长度、偏移、密钥比对。优化代码一定要配参照物测试。

## 4. `violation.go`：违规清单与角色

- `Role` 决定掩码规则：客户端帧必须有掩码，服务端帧必须没有（`mask_missing` / `mask_unexpected`）。所以判断一个帧必须知道**谁发的**。
- RSV1 只有协商了 permessage-deflate（RFC 7692）才合法，而且只能出现在数据消息的第一帧；续帧和控制帧上的 RSV1 仍然非法。这就是 `Rules.AllowRSV1` 的由来。
- 控制帧三条规则（RFC 6455 §5.5）：不能分片、载荷 ≤125、可以插在分片消息之间。
- `Violation.String()` 的值进入事件 Schema，是**对外契约**，所以有测试锁住名字，别随便改。

## 5. `utf8.go`：增量 UTF-8 校验

文本消息可以在任意字节处分片，包括一个多字节字符的中间，所以：

- 对每个分片单独 `utf8.Valid` 会**误杀**合法消息；
- 等到最后一片再校验又会让对端逼我们缓冲大量垃圾。Autobahn 第 6 组要求“看到非法字节就立刻失败”。

做法：最多保留 3 字节的“未完成字符”。关键 API 是 `utf8.FullRune`——它对**永远不可能合法**的序列（如 `C0 80`）也返回 true，这正好让我们在还没收完时就能判定失败。验证方式是模糊测试 `FuzzUTF8Chunking`：任意切分的结论必须等于标准库 `utf8.Valid` 对整体的结论。

## 6. `assembler.go`：分片与状态机

核心状态只有一个：当前是否在一个分片消息里（`inMessage`）。

- 续帧但不在消息里 → `unexpected_continuation`；消息中间又来新的文本/二进制帧 → `interleaved_data`；两者都是 1002。
- **控制帧完全不碰这个状态**，因为协议允许它们插在分片之间（`TestAssemblerFragmentedWithInterleavedControl`）。
- 单帧消息（最常见）直接沿用解码器的切片，不复制。
- 压缩消息（RSV1）不在这里做 UTF-8 校验，因为必须先解压；这个责任留给 M2 的适配器，字段 `Message.Compressed` 就是交接标记。
- `ProtocolError.CloseCode` 直接给出该发的关闭码：协议错误 1002、数据不合法 1007、消息太大 1009。
- `ParseClose`：载荷必须是 0 字节或 ≥2 字节；1004/1005/1006/1015 只能本地使用，不能出现在线上。

## 7. `sse/parser.go`：分块无关性

SSE 的规范是 WHATWG HTML §9.2。网络读取可以在任何位置结束：行中间、CRLF 之间、BOM 中间、多字节字符中间。所以解析器显式保存状态：

| 状态 | 解决什么 |
| --- | --- |
| `line` | 没读完的一行 |
| `skipLF` | 上一块以 CR 结尾，下一块开头的 LF 属于同一个行结束符 |
| `head` | 流开头还在判断是不是 BOM |
| `data` / `eventType` / `lastID` | 事件缓冲；**`lastID` 跨事件保留**，这是重连时 `Last-Event-ID` 的来源 |

要点：

- 三种行结束符 `\n`、`\r`、`\r\n` 都合法；CRLF 当作**一个**结束符，否则会多出一个空行从而错误分派事件。
- 分派规则：空行触发，只有数据缓冲非空才产出事件；每个 `data` 行追加值和 `\n`，分派时去掉最后一个 `\n`，所以 `data` 行为空也算一个事件。
- `id` 含 NUL 要忽略；`retry` 只接受纯 ASCII 数字（带符号、空值都忽略）；冒号后**只去掉一个**空格。
- 调试器与浏览器不同：被忽略的东西（拼错的字段名、非法 `retry`、流结束时丢弃的半个事件）会作为 `ItemIgnored` / `ItemDiscarded` 暴露出来，用户才知道“为什么我的事件没触发”。
- 已知偏差（写在代码注释里）：非法 UTF-8 我们是逐字节替换成 U+FFFD，规范是按“最大不合法子序列”替换，只在被截断的多字节序列上有区别。

测试的价值排序：`TestEveryTwoWaySplitGivesSameResult` 与 `FuzzChunkingIndependence` 最重要——它们不描述“答案是什么”，而是描述“无论怎么切，答案都一样”，这就是**性质测试（property-based testing）**。

## 8. 测试方法清单（面试可讲）

| 方法 | 在哪 | 抓什么问题 |
| --- | --- | --- |
| 规范向量 | `TestRFC6455Examples` | 读错规范 |
| 边界值 | `TestLengthBoundaries` | off-by-one |
| 参照物比对 | `TestMaskMatchesNaive` | 优化引入的错误 |
| 往返性质 | `TestEncodeDecodeProperty`、`FuzzDecoder` | 编码解码不一致；顺带证明“字节级还原” |
| 切分无关性 | `FuzzUTF8Chunking`、`FuzzChunkingIndependence` | 状态没保存好 |
| 模糊测试 | 4 个 `Fuzz*` | panic、无界内存、逻辑漏洞 |
| 检查器自测 | `TestCheckerCatchesViolations` | 依赖方向检查本身是空转的 |

## 9. 依赖方向检查（`internal/archtest`）

用 `go/parser` 只解析 import（`parser.ImportsOnly`，即使代码暂时编译不过也能跑），按层号比较：只允许依赖严格更低的层，同层互不引用；`codec` 层还禁止第三方依赖。它是普通测试，所以违规会像单元测试失败一样让 CI 变红。`TestCheckerCatchesViolations` 故意构造违规输入，防止检查器因为扫描不到文件而“永远通过”。

## 10. 自己动手的练习

1. 在 `Header.Violations` 里把 `MaskMissing` 的判断删掉，看哪些测试变红，再恢复。
2. 把 `MaskAt` 的快路径改成按 4 字节一组，写个基准 `BenchmarkMask` 比较。
3. 给 `sse.Parser` 加一个 `MaxCommentBytes` 限制，并补上相应的测试。
4. 阅读 RFC 6455 §5.4 与 §5.5，找出本实现里还没有覆盖的一条规则（提示：关闭握手之后的帧）。

## 参考

- RFC 6455 §5.2 帧格式、§5.3 掩码、§5.4 分片、§5.5 控制帧、§5.7 示例、§7.4 状态码、§8.1 UTF-8
- RFC 7692 permessage-deflate（RSV1 的用法）
- WHATWG HTML Living Standard §9.2 Server-sent events（事件流解析算法）
- Autobahn|Testsuite（WebSocket 一致性测试，分组：1 框架、2 ping/pong、5 分片、6 UTF-8、7 关闭）
