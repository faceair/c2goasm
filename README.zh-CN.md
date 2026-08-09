# c2goasm

[English](README.md) | [简体中文](README.zh-CN.md)

[![CI](https://github.com/faceair/c2goasm/actions/workflows/ci.yml/badge.svg)](https://github.com/faceair/c2goasm/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

`c2goasm` 把编译器生成的 64 位 C/C++ 汇编转换成 Go Plan 9 汇编，保留平台 C ABI。简单说：性能关键的代码你用 C 写，让 clang/gcc 做指令选择和向量化，剩下的交给 c2goasm —— 编译器输出的汇编会被转成 Go 汇编，直接编译进你的 Go 程序。

根据函数形态，有两种接入方式：

- **直接调用（direct-safe leaf）**：无栈帧、无调用的纯计算函数，经静态认证后获得 Go ABI wrapper，`CGO_ENABLED=0` 即可运行。
- **完整 native graph**：QuickJS、SQLite、PCRE2 这类完整 C 库，通过标准 cgo trampoline 进入，之后内部调用图全部按原生 C ABI 执行。

两种模式泾渭分明，没有隐晦的降级路径：请求直接调用却无法通过认证时，转换直接失败并指出函数、源码行与原因。

## 典型场景：在 Go 里用上 SIMD

Go 编译器目前不做自动向量化，想在 Go 里获得 NEON/AVX 性能，通常只有三条路，都不好走：

- **手写 Plan 9 汇编**：学习成本高，还要自己维护寄存器分配、指令编码与 ABI 细节；
- **纯 Go 循环碰运气**：标量执行，SIMD 加速无从谈起；
- **cgo 调 C 库**：每次调用有跨语言边界开销，还要求目标机器有 cgo 工具链。

`c2goasm` 提供第四条路：**用 C 写内核，让 C 编译器生成 SIMD 汇编，再转成 Go 汇编**。C 编译器几十年来一直在优化 SIMD —— 自动向量化、intrinsics、循环展开都极其成熟，你只需要把精力放在算法本身。

工作流大致是：

```text
kernel.c（intrinsics 或普通循环）
   │  clang/gcc -O3
   ▼
kernel.s（C ABI 汇编）
   │  c2goasm
   ▼
kernel.s（Go Plan 9 汇编，编译进你的包）
```

### 一个可运行的最小示例

完整可运行的示例代码在 [`examples/simd/`](examples/simd/) —— 双架构的转换汇编已提交，无需 C 工具链即可直接 `go test`。下面的代码就是同一个示例的逐步说明。

用 C 写两个内核：一个求和（展示返回值形态），一个逐元素相加（展示输出指针形态）：

```c
// simd.c
#include <stdint.h>

// 返回值形态：uint64_t 是单个 word 返回值，direct-safe 认证直接支持。
uint64_t sum_u32(const uint32_t *input, uint64_t size) {
    uint64_t sum = 0;
    #pragma clang loop vectorize(enable) interleave(enable)
    for (uint64_t i = 0; i < size; i++) {
        sum += input[i];
    }
    return sum;
}

// 输出指针形态：写回 dst，无返回值，适合批量 kernel。
void add_u32(const uint32_t *a, const uint32_t *b, uint32_t *dst, uint64_t size) {
    #pragma clang loop vectorize(enable) interleave(enable)
    for (uint64_t i = 0; i < size; i++) {
        dst[i] = a[i] + b[i];
    }
}
```

`#pragma clang loop vectorize(enable)` 是显式请求向量化，clang 和 gcc 都认。把源文件放进 `input/` 子目录，避免 `.c`/`.s` 输入文件被 Go 工具链当成包源码。

companion Go 文件里的 bodyless 函数（`_sum_u32`、`_add_u32`）是转换的入口：它们显式请求 c2goasm 为对应 C 符号生成 Go ABI wrapper。

```go
//go:build arm64 && !nosimd

package simd

import (
    "runtime"
    "unsafe"
)

func _sum_u32(input unsafe.Pointer, size uint64) uint64
func _add_u32(a unsafe.Pointer, b unsafe.Pointer, dst unsafe.Pointer, size uint64)

func SumU32(input []uint32) uint64 {
    if len(input) == 0 {
        return 0
    }
    result := _sum_u32(unsafe.Pointer(&input[0]), uint64(len(input)))
    runtime.KeepAlive(input)
    return result
}

func AddU32(dst, a, b []uint32) {
    _add_u32(unsafe.Pointer(&a[0]), unsafe.Pointer(&b[0]), unsafe.Pointer(&dst[0]), uint64(len(a)))
    runtime.KeepAlive(a)
    runtime.KeepAlive(b)
    runtime.KeepAlive(dst)
}
```

AMD64 平台用同样的 companion 签名，只是把 build tag 换成 `//go:build amd64 && !nosimd` 单独一个文件。用 `-tags=nosimd` 编译时提供一份纯 Go 的标量实现作为回退（同样用 build tag 组织，比如 `//go:build nosimd` 的 `generic.go`），这样不支持 SIMD 的 CPU 也能跑：

```go
//go:build nosimd

package simd

func SumU32(input []uint32) uint64 {
    var sum uint64
    for _, v := range input {
        sum += uint64(v)
    }
    return sum
}

func AddU32(dst, a, b []uint32) {
    for i, v := range a {
        dst[i] = v + b[i]
    }
}
```

编译并转换（Darwin/arm64，NEON）：

```bash
clang -S -O3 \
  -fomit-frame-pointer -fno-stack-protector \
  -fno-asynchronous-unwind-tables -fno-unwind-tables \
  -mno-outline -ffixed-x27 -ffixed-x28 \
  --target=arm64-apple-darwin \
  -o input/simd.s simd.c

c2goasm -t arm64 input/simd.s simd.s
CGO_ENABLED=0 go test ./...
```

Linux/amd64（AVX2）同样直接可用：

```bash
gcc -S -O3 -mavx2 -masm=intel \
  -fomit-frame-pointer -fno-stack-protector -mno-red-zone \
  -fno-asynchronous-unwind-tables -fno-unwind-tables \
  -ffixed-rbp -ffixed-r14 -ffixed-r11 \
  -o input/simd.s simd.c

c2goasm -t amd64 input/simd.s simd.s
CGO_ENABLED=0 go test ./...
```

`-ffixed-x27 -ffixed-x28`（ARM64）和 `-ffixed-rbp -ffixed-r14 -ffixed-r11`（AMD64）是 Go 运行时保留寄存器，C 编译器必须避开它们。编译器必须真正生成无栈帧的 leaf —— 优化参数不保证结果，转换器会逐条验证生成的指令。

### 效果

在 Apple M1 Pro（arm64/NEON）上，对 4 MiB 输入实测：`sum_u32` 比纯 Go 标量循环快约 6 倍，`add_u32` 约 3 倍，两种内核都是 0 分配、`CGO_ENABLED=0` 下运行。AMD64（AVX2，gcc 15）上同样有 2–3 倍提升。

具体倍数取决于 CPU 微架构与内存带宽：求和这类带宽敏感操作，在内存带宽充裕的机器上提升更明显；逐元素运算则受限于读写总量。[`examples/simd/simd_bench_test.go`](examples/simd/simd_bench_test.go) 里的 benchmark（generic 与 SIMD 配对、报告 speedup）可以直接在你的目标硬件上复测。这套"标量基线 + build tag 回退 + speedup 对比"的组织方式，已经在生产项目（如 GuanceDB 的 executor）中落地。

## 直接调用（direct-safe leaf）的边界

直接调用模式只接受"经过认证的纯计算 leaf"，认证通过时才生成 wrapper：

- 函数必须无栈帧操作（不读写 SP，不使用栈上内存或 red zone）；
- 不能有 call、间接跳转或跳出函数自身的控制流；
- 不能碰 TLS、系统寄存器、syscall 或 Go 运行时保留寄存器；
- 参数必须是整数或指针（AMD64 最多 6 个、ARM64 最多 8 个），返回值至多一个同类型 word；
- companion 签名必须与 C 原型逐字一致，传入的 Go 内存要存活到返回（必要时用 `runtime.KeepAlive`）。

不满足以上条件时转换直接失败。需要浮点参数、聚合类型、多返回值、分配器或回调的场景，请走下面的完整 native graph 模式。

## 完整 C 库：native graph

QuickJS、SQLite、PCRE2 这类库包含动态栈帧、深调用图、分配器和 pthread 状态，无法在 goroutine 栈上直接运行。转换完整模块后，通过 `github.com/faceair/c2goasm/nativecall` 提供的少量入口进入：

- `Call0(address uintptr) int32`、`CallBytes(address uintptr, value []byte) int32`
- `InstallMemory(address uintptr)`：注入进程分配器
- `InstallThreads(address uintptr)`：注入 pthread 互斥操作

完整的集成示例可以直接运行：

- `scripts/quickjs-e2e.sh`、`scripts/pcre2-e2e.sh`、`scripts/sqlite-e2e.sh`、`scripts/amd64-cutils-e2e.sh`

其中 PCRE2 示例转换全部 31 个 8-bit 核心 translation unit（启用 Unicode、禁用 JIT），并验证配置、表生成、UTF/UCP 匹配、编译错误与并发调用。这些脚本是参考集成 —— 转换任意 C 源码前，仍要针对它的宿主边界（libc、locale、线程模型）做适配。

## 为什么用 c2goasm

- **让 C 编译器干它最擅长的活**：指令选择、寄存器分配、向量化都交给 clang/gcc，而不是手工翻译成 Plan 9 汇编。
- **汇编可复现**：同样的 C 源码与编译参数，总能得到同样的 Plan 9 汇编，不再有手写汇编的维护漂移。
- **按需选择运行时边界**：小 leaf 可以完全不依赖 cgo；完整 C 库用标准 cgo 进入，而不是把 goroutine 栈冒充 C 栈。
- **失败即终止**：未解析的重定位、不支持的指令、符号冲突、ABI 越界或不合格的直接入口都会终止转换，并给出函数名、源码行和原因，不存在静默降级。
- **结构化管线**：`parse → analyze → rewrite → encode → emit` 基于 IR 而不是正则替换，便于审计与扩展。
- **无需分发动态库**：转换后的代码链接进 Go 产物；native graph 模式虽然需要 cgo，但不要求随包分发 `.so`/`.dylib`。
- **双架构**：GCC Intel 语法（AMD64）与 Clang GNU 语法（ARM64）都是生产路径。

## 与 minio/c2goasm 的关系

本项目延续了已归档的 [minio/c2goasm](https://github.com/minio/c2goasm) 的核心思路：先由 C/C++ 编译器生成汇编，再转换成 Go 汇编。它是独立重写，不是兼容 fork。

| 维度 | minio/c2goasm | github.com/faceair/c2goasm |
|---|---|---|
| 架构 | AMD64 | AMD64 与 ARM64 |
| 编译器输入 | 主要是 Clang Intel 语法 | AMD64 用 GCC Intel 语法；ARM64 用 Clang GNU 语法 |
| 转换模型 | 面向行转换 + asm2plan9s | 结构化 IR、全程序标签/重定位重写、三级指令编码 |
| 内部调用 | 通常要求 inline/call-free | native graph 模式支持完整内部 C 调用图 |
| 直接入口 | 代码直接发射为 Go 汇编函数 | 仅显式请求且通过静态认证的 leaf |
| 运行时边界 | 以直接 wrapper 为中心 | cgo-free leaf 与标准 cgo native graph 分离 |
| 验证 | 聚焦 SIMD 示例 | Direct leaf、QuickJS、SQLite、PCRE2、cutils、并发、GC、栈增长 E2E |
| 失败模型 | 转换文档化支持子集 | 带上下文的 fail-fast，无 legacy/unsafe 降级 |
| License | Apache-2.0 | Apache-2.0 |

## 与 cgo 的对比

`c2goasm` 不是 cgo 的通用替代品，两者解决不同的问题。

| 属性 | direct-safe leaf | native graph | 直接 cgo |
|---|---|---|---|
| `CGO_ENABLED=0` | 可以 | 不可以 | 不可以 |
| 跨语言边界切换 | 无 | 有（`runtime.cgocall`） | 有 |
| C 代码使用的栈 | 当前 goroutine 栈（因此必须无 frame） | cgo/system 栈 | cgo/system 栈 |
| C 调用、TLS、errno、分配器、pthread | 拒绝 | 支持（经声明的 native 边界） | 支持 |
| 项目动态库 | 不需要 | 不需要 | 取决于链接方式 |
| 最适合 | 小型有界计算/扫描 | 转换后的引擎与完整模块 | 常规 C 互操作 |

需要注意：native graph 不会消除 Go→C 的切换成本（仍然经过 `runtime.cgocall`），它的价值在于把转换后的代码纳入 Go 构建、并在一次进入后让内部调用图全程保持原生 C ABI。直接 cgo 进入 C 后，C 内部调用同样是原生的 —— 本项目不把这点宣传成额外优势。

## 支持的工具链

- Go 1.26
- `amd64`：System V AMD64 ABI，GCC Intel 汇编
- `arm64`：AAPCS64，Clang GNU 汇编
- 已验证运行宿主：Linux/amd64、Darwin/arm64
- Python 3 仅用于 integration 脚本下载并准备上游测试源码

## 安装

```bash
go install github.com/faceair/c2goasm/cmd/c2goasm@latest
```

或从仓库构建：

```bash
git clone https://github.com/faceair/c2goasm.git
cd c2goasm
go build -o ./bin/c2goasm ./cmd/c2goasm
```

## CLI

```text
c2goasm [-t amd64|arm64] [-s] [-c] [-f] input.s output.s
```

| 选项 | 含义 |
|---|---|
| `-t` | 输入架构。默认 `amd64`；接受 `amd64`/`x86`/`x86_64`/`x86-64` 与 `arm64`/`aarch64`。 |
| `-s` | 去掉生成的指令注释。 |
| `-c` | 合并相邻字节字面量。 |
| `-f` | 用 `PATH` 中的 `asmfmt` 格式化输出。 |
| `-a` | 兼容性 no-op；指令编码始终启用。 |

输出文件必须以 `.s` 结尾，且同目录必须存在同名的 companion Go 文件（例如 `kernel.s` 对应 `kernel.go`）。companion 提供 Go 声明，并让每个直接入口成为显式请求。

## 输入约束

转换接受编译器生成的 64 位 GNU/Intel 汇编子集，不是任意手写汇编：

- 函数边界必须可通过 `.globl`、ELF `.size` 或 Clang `Begin function` 标记识别；
- 需要禁用 jump table；建议禁用 stack protector、unwind table 与 compiler outlining；
- ARM64 输入必须保留 X27/X28；AMD64 必须保留 RBP/R14（完整 graph 还要保留 R11）；
- 不支持 `setjmp`/`longjmp`、C++ 异常/unwind、Go 回调、动态加载语义与任意 TLS；
- 规范化后每个 Go/native 符号必须唯一，冲突直接终止转换。

编译参数是 ABI 契约的一部分：从 E2E 脚本中的参数出发，任何修改都要有对应的验证。

## 测试与 CI

快速本地检查：

```bash
go test -count=1 ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/c2goasm
```

Native 集成验证：

```bash
./scripts/direct-leaf-e2e.sh
./scripts/quickjs-e2e.sh
./scripts/pcre2-e2e.sh
./scripts/sqlite-e2e.sh          # Darwin/arm64
./scripts/amd64-cutils-e2e.sh    # Linux/amd64
```

仓库不保存大型第三方源码。Integration 脚本下载并校验官方固定版本的 QuickJS 2026-06-04、PCRE2 10.47 与 SQLite 3.48.0（SHA-256 固定），解压到：

```text
${C2GOASM_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/c2goasm}/sources
```

也可以用 `QUICKJS_SOURCE_DIR`、`PCRE2_SOURCE_DIR`、`SQLITE_SOURCE_DIR` 指向已有的可信源码。生成的汇编与测试模块只存在于临时目录。GitHub Actions 在 Linux/amd64 与 Darwin/arm64 上运行单元/构建检查与完整的 native matrix。

## 仓库结构

```text
arch/                 平台 C ABI 与寄存器描述
cmd/c2goasm/          CLI
examples/simd/        可运行的 SIMD 示例（C 内核 → 转换汇编 → Go API）
internal/asm/         解析、IR、分析、重写、direct 认证、发射
internal/asm2plan9s/  指令汇编、反汇编与字节回退
nativecall/           标准 cgo 入口、分配器与 pthread 边界
scripts/              可复现的 direct 与 full-graph E2E
.github/workflows/    Linux/amd64 与 Darwin/arm64 CI
```

## 贡献

欢迎提交 bug 报告和范围明确的 pull request。转换失败时请附上编译器版本、目标架构、出问题的源指令和最小可复现输入。新增指令或重定位支持必须带行为或编码回归测试，并保持 fail-fast。

## License

[Apache-2.0](LICENSE) © 2026 faceair。

Integration 测试下载的第三方源码继续遵循各自的上游许可证。
