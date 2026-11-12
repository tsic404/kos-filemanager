# kos-filemanager

KOS 系统应用项目：扩展包 — 授权卷内的文件浏览与传输。

- 设计：`tsic/kos-design` 仓的 `kos-filemanager/`（范围、契约、验收、决策记录）
- 设计依据：`tsic/kos-design@05bf60a`

## 边界

- 数据面只服务授权卷：卷的授权取所有者（`KOSD-C-60`），偏好不扩大授权（`FILE-C-1`）。
- 数据面只接受窗口实例的应用令牌，不使用控制台会话 Cookie，也不接受集群凭据（`KOSD-C-75`、`KOSD-F-9`）。
- 卷的生命周期、所有者与入口归属由 kosd 承载；本产品不管理卷、不创建 PVC，不提供通用文件读写接口（`KOSD-NG-10`）。
- 装载与卸载走扩展包的同一条通道，没有专用安装路径（`KOSD-C-17`）。

## 模块

| 路径 | 内容 | 契约 |
|---|---|---|
| `pkg/product/` | 产品标识、`App` 清单、扩展声明、UI 资产与经 SDK 的打包 | `FILE-C-1`，引用 `KOSD-C-17`、`KOSD-C-18`、`KOSD-C-73`、`SDK-C-3`、`SDK-C-4` |
| `pkg/product/assets/` | 窗口入口文档、视图模块、样式与图标；随包分发 | `KOSD-C-73`、`KOSD-C-75`、`DASH-C-27` |
| `pkg/dataplane/` | 自有数据面：应用令牌鉴权、授权卷列表与卷状态、偏好读取 | `FILE-C-1`、`FILE-C-2`，引用 `KOSD-C-13`、`KOSD-C-38`、`KOSD-C-60`、`KOSD-C-75` |
| `cmd/kos-filemanager-pack/` | 构建带摘要的安装包 | `KOSD-C-17`、`SDK-C-4` |
| `tests/acceptance/<TC-id>/` | 验收用例，目录名取设计编号；`shell.test.js` 是用例的窗口侧部分 | `03-acceptance.md` 的对应条目 |

### 窗口

`pkg/product/assets/` 是随包分发的窗口资产：入口文档、视图模块、样式与清单声明的图标。窗口被窗口宿主装载后完成以下约定。

- 从帧地址的 `kos-console` 与 `kos-window` 读取控制台来源和窗口实例，向该来源发 `ready`；只接受同一来源、同一父窗口的 `init`，且只接受一次（`KOSD-C-75`）。
- 应用令牌只留在模块闭包内：不写存储、不进文档、不回传消息；数据面请求带 Bearer 令牌且 `credentials: "omit"`，控制台会话 Cookie 不参与（`KOSD-C-75`、KOSD-F-18）。
- 授权卷列表来自本应用的 `/files/window` 应答，窗口不自行推断或补全卷；加载中、空列表、读取失败与令牌失效各有可读状态（`FILE-C-1`）。
- 列表与网格是同一份授权卷的两种呈现；初始视图与初始路径取应答中的偏好（`DASH-C-27`），切换视图只是本地呈现，不写偏好。

### 数据面

`pkg/dataplane` 是产品自己的服务，不是控制面 API 的一部分（`KOSD-NG-10`）：

| 路由 | 语义 | 凭据 |
|---|---|---|
| `GET /healthz` | 清单声明的 `backend.health` 地址 | 无 |
| `GET /files/window` | 视图偏好与授权卷列表，逐卷带状态、可浏览性与原因 | 应用令牌 |
| `GET /files/volumes/{namespace}/{name}` | 单卷状态；未绑定与丢失返回 409 与原因 | 应用令牌 |

- 授权只按卷的 `spec.owner`：其他账号的卷不出现在列表里，对其单片请求返回 403；全局 Owner 的转移能力是控制面写入，不扩大数据面授权（`FILE-C-1`）。
- 每次请求重新读取事实，不建索引：卷恢复为 `Bound` 后，同一窗口的下一次浏览即可操作（`FILE-C-2`）。
- 卷状态取投影：`phase` 来自卷状态，观测时间超过 2 分钟标记 `stale` 且健康为 `Unknown`，不回写存储（`KOSD-C-13`、`KOSD-C-38`）。
- 应用令牌由窗口宿主签发与撤销；`dataplane.AppHost` 是对 `kos-dashboard` 应用宿主的适配。数据面不读取 Cookie，也不接受集群凭据。

### 打包

`make package` 经 kos-sdk 的打包路径产出 `dist/kos-filemanager-<version>.tar.gz` 与同名 `.digest`。包结构固定为 `manifest.json`、`digest` 与 `assets/`；摘要覆盖清单与全部资产，清单声明的 `icon` 必须随包提供（`SDK-C-4`、`KOSD-C-73`）。

入口 origin 是构建输入（`-origin`，默认 `https://filemanager.kos.local`）。清单声明的 `entry` 是该 origin 上的**文档地址** `https://<origin>/assets/index.html`：宿主把 `entry.Path` 当作包内资产路径，入口必须指向包里携带的文档，打包时校验（`build.Entry`、`checkEntryCarried`）。origin 变化改变清单，因此改变包摘要。

最终外部形态：

| 地址 | 形态 |
|---|---|
| 文档 | `https://<origin>/assets/index.html` |
| 子资产 | `https://<origin>/assets/app.css`、`.../app.js`、`.../icon.svg`，随文档相对解析 |
| 数据面 | `https://<origin>/files/window`、`.../files/volumes/{namespace}/{name}`、`.../healthz` |

宿主按 `KOSD-C-73` 与 `KOSD-C-84` 分发这些资产：地址是 `/app-assets/<扩展>/<应用>/<版本>/<摘要>/<资产路径>`，签名只绑扩展、应用、版本、摘要与到期，路径不入签名；入口文档验签后随应答设置 `kos_asset_grant` Cookie（`Path=/app-assets/`、HttpOnly），同摘要的相对子资源凭它加载，不再各自带签名。因此文档里的 `app.css`、`app.js`、`icon.svg` 保持相对引用，不需要也不应该写绝对 origin。

### 装载与卸载

本仓产出的包由 kosd 的安装路径摄取：`manifest.json`、`assets/` 与 `digest`（`KOSD-C-17`），摘要不匹配时拒绝；`apps` 类随组件激活装载，`GET /api/v1/apps` 把扩展、组件、阶段、摘要与清单交给窗口宿主。安装后窗口出现在投影与窗口宿主里，卸载后同时消失，其余产品与内核对象不变。

`tests/acceptance/KOSD-TC-155/` 用这条真实路径取证：以 SDK 打出的包发起安装、读取投影与窗口宿主、卸载后再读，并用摘要不符的请求验证拒绝面；资产面用 `kos-dashboard` 的资产服务与签名地址逐个取回入口文档和三个相对子资源。

数据面的目标部署形态是独立服务：它有自己的集群凭据；控制台 BFF 不持有集群凭据（`KOSD-C-12`），因此不能把数据面挂在 BFF 进程里。`dataplane.AppHost` 只是同进程可验的生命周期适配，独立部署还缺应用令牌的跨进程校验通道（控制面身份接受应用令牌，或窗口宿主提供 introspection）；两条都需要跨项目契约。

## 构建与测试

```sh
make lint        # gofmt + go vet + node --check
make test        # 窗口浏览器用例 + 单元与验收用例，需要 envtest（自动下载并校验 sha512）
make acceptance  # 只跑 tests/acceptance 的 Go 部分，一条命令
make js-test     # 只跑窗口的浏览器侧用例，需要 node 22+
make package     # 构建安装包与摘要
```

| 目录 | 判据 |
|---|---|
| `tests/acceptance/FILE-TC-1/` | 列表只含自己拥有的卷；偏好中的他人卷仍可读但不进列表；第二个会话只看自己的卷；`shell.test.js` 校验窗口按存储的视图与初始路径渲染该列表 |
| `tests/acceptance/FILE-TC-2/` | 关窗后令牌被拒；控制台会话 Cookie 被拒；产品产出的入口对象指向包内声明的应用；窗口声明给出默认/最小尺寸、单实例与默认状态；`shell.test.js` 校验握手来源、令牌使用与失效提示 |
| `tests/acceptance/FILE-TC-3/` | 已绑定、未绑定与丢失三态可区分；不可用时 409 且原因可读；恢复后下一次浏览可操作 |
| `tests/acceptance/FILE-TC-8/` | 转移后原所有者列表不再含该卷，新所有者可浏览 |
| `tests/acceptance/KOSD-TC-155/` | 系统组件装载与卸载：窗口出现在控制面投影与窗口宿主、卸载后消失、摘要不符被拒；资产服务按签名地址返回入口文档与相对子资源；偏好数据按声明撤除，其余产品与内核对象不变 |
| `tests/shell/harness.js` | 窗口用例共用的浏览器替身（文档、帧窗口、fetch、时钟） |

## 依赖

- kosd：卷与所有者、控制面 API、偏好存储、扩展声明与安装路径。
- kos-dashboard：窗口宿主、应用令牌与窗口会话。
- kos-sdk：清单校验与打包工具。
