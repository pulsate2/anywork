// 剪贴板写入。navigator.clipboard 只在安全上下文(HTTPS / localhost)里存在,
// 用 http:// 访问局域网 IP 时整个 API 都是 undefined —— 手机上恰恰是这种场景,
// 所以留一条 execCommand 的后路,两条都不通再报错。
export async function copyText(text: string): Promise<boolean> {
  if (!text) return false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 权限被拒或非安全上下文,落到下面的后路。
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.readOnly = true
    // 不能 display:none / visibility:hidden —— 选不中的元素复制不出去。
    ta.style.cssText = 'position:fixed;top:0;left:-9999px;opacity:0'
    document.body.appendChild(ta)
    ta.select()
    ta.setSelectionRange(0, text.length)
    const ok = document.execCommand('copy')
    ta.remove()
    return ok
  } catch {
    return false
  }
}

// selectAllIn 把整个元素的文本选上,让用户直接用系统的长按菜单复制。
// 两条写入路径都被浏览器拦下时,这是最后一条还能走的路。
export function selectAllIn(el: HTMLElement): void {
  const sel = window.getSelection()
  if (!sel) return
  const range = document.createRange()
  range.selectNodeContents(el)
  sel.removeAllRanges()
  sel.addRange(range)
}
