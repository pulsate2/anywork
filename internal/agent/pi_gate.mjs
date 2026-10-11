// anywork 审批 gate —— 注入 pi 的扩展(pi --mode rpc -e <此文件>)。
//
// pi 的 rpc 模式原生对所有工具调用自动放行,没有审批通道。这个扩展用
// `tool_call` 钩子(“Fired before a tool executes. Can block.”)在有副作用的
// 工具执行前,经扩展 UI 的 select 向宿主(anywork)发一次询问:select 的
// title 是宿主识别用的 sentinel-JSON(含工具名与入参),options 是三态令牌。
// 宿主把它渲染成审批卡,答复经 extension_ui_response.value 回来:
//   - allow-session:本会话该【类】后续不再问 —— 编辑类是一整类(选了写文件的
//     本会话允许,edit/replace_match/copy/move… 都不再问,即"切到允许编辑状态");
//     bash 自成一类(放行范围本就宽)
//   - allow-once:仅放行这一次
//   - deny / 取消:返回 { block: true },pi 不执行该工具
// 宿主不可交互(hasUI=false)或询问抛错时,退回 pi 默认行为(放行),不阻断。

// 编辑/改文件工具:pi 1.1 把编辑拆成多个工具名,但对用户是同一件事("改文件")。
// 本会话允许任一个 = 整个编辑类放行,不再为别的编辑工具逐个弹卡。
const EDIT = new Set([
  "write", // 创建/覆盖文件
  "edit", "replace", "replace_match", "insert", // 编辑文件(pi 1.1 主力是 edit)
  "copy", "move", // 搬运/复制行
  "undo_last_change", // 撤销上一次改动
])

const DANGEROUS = new Set([...EDIT, "bash"]) // bash:执行 shell 命令

// 本会话允许的归类键:编辑类统一记 "edit",其余(bash)按工具名各记各的。
function categoryOf(tool) {
  return EDIT.has(tool) ? "edit" : tool
}

const DENY = "deny"
const ALLOW_ONCE = "allow-once"
const ALLOW_SESSION = "allow-session"

export default function (pi) {
  const sessionAllowed = new Set() // 本会话已选"本会话允许"的类别键(categoryOf)

  pi.on("tool_call", async (event, ctx) => {
    const tool = event.toolName
    if (!DANGEROUS.has(tool)) return
    if (sessionAllowed.has(categoryOf(tool))) return
    if (!ctx.hasUI) return // 没有可交互宿主:退回默认放行

    const title = JSON.stringify({ __anyworkApprove: 1, tool, input: event.input })
    let choice
    try {
      choice = await ctx.ui.select(title, [DENY, ALLOW_ONCE, ALLOW_SESSION], {})
    } catch {
      return // 询问失败:不阻断
    }
    if (choice === ALLOW_SESSION) {
      sessionAllowed.add(categoryOf(tool)) // 编辑类整类放行;bash 只放 bash
      return
    }
    if (choice === ALLOW_ONCE) return
    // deny 或 undefined(取消)→ 阻断
    return { block: true, reason: "用户在 anywork 里拒绝了此操作" }
  })
}
