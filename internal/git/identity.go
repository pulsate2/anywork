package git

import "strings"

// 提交身份(user.name / user.email)。
//
// git 在这两项都推断不出来的时候会直接拒绝提交,并打印那段著名的
// "*** Please tell me who you are." —— 而它给的解法是让人去敲 git config。
// 服务端这边没有终端可敲,所以改成:动手之前先问一句 git 现在能不能拼出身份,
// 不能就回 ErrNoIdentity(HTTP 428),前端弹框让用户填,填完把刚才那个动作重放。
//
// 写入一律只写**仓库级**(git config --local):一台机器上不同仓库要用不同身份是常事,
// 而写全局等于替用户改掉他所有仓库的默认值,那不是他按下这个按钮的意思。
// (链接 worktree 里的 --local 落到主仓库那份共享 .git/config,这是 git 的语义 ——
// 同一个仓库的多个工作树本来就共用配置,不是这里漏了什么。)

// Identity 是一个仓库的提交身份。
type Identity struct {
	// Name/Email 是生效值:仓库级没写就是从全局/系统继承来的那份。
	Name  string `json:"name"`
	Email string `json:"email"`
	// LocalName/LocalEmail 只看这个仓库自己的 .git/config。前端据此说清楚
	// "这份身份是本仓库设的还是继承来的",别让人以为改的是全局。
	OK         bool   `json:"ok"`
	LocalName  string `json:"localName"`
	LocalEmail string `json:"localEmail"`
}

// identityMax 名字/邮箱的长度上限。git 自己不管,这里拦一下明显不像身份的输入。
const identityMax = 200

// Identity 读取仓库的提交身份。
func (s *Service) Identity(p string) (Identity, error) {
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return Identity{}, err
	}
	if !info.Repo {
		return Identity{}, ErrNotRepo
	}
	return s.identityAt(info.Root), nil
}

// SetIdentity 把提交身份写进这一个仓库的 .git/config,并返回写完之后的状态。
func (s *Service) SetIdentity(p, name, email string) (Identity, error) {
	if err := s.allowWrite(); err != nil {
		return Identity{}, err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return Identity{}, err
	}
	if !info.Repo {
		return Identity{}, ErrNotRepo
	}
	n, e, err := checkIdentity(name, email)
	if err != nil {
		return Identity{}, err
	}
	// -- 之后才是键和值,否则以 - 开头的值(有人真会把邮箱填成 -a@b.c)会被当成选项。
	if _, err := s.run(info.Root, nil, "config", "--local", "--", "user.name", n); err != nil {
		return Identity{}, err
	}
	if _, err := s.run(info.Root, nil, "config", "--local", "--", "user.email", e); err != nil {
		return Identity{}, err
	}
	return s.identityAt(info.Root), nil
}

func (s *Service) identityAt(root string) Identity {
	return Identity{
		Name:       s.configGet(root, false, "user.name"),
		Email:      s.configGet(root, false, "user.email"),
		LocalName:  s.configGet(root, true, "user.name"),
		LocalEmail: s.configGet(root, true, "user.email"),
		OK:         s.identityOK(root),
	}
}

// configGet 读一个配置项。local=true 只看仓库级,否则取生效值(仓库 → 全局 → 系统)。
// 没设置时 git 用退出码 1 表示,这里统一折成空串。
func (s *Service) configGet(root string, local bool, key string) string {
	args := []string{"config"}
	if local {
		args = append(args, "--local")
	}
	args = append(args, "--get", "--", key)
	out, err := s.run(root, nil, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// identityOK 直接问 git「你现在能不能拼出提交身份」,而不是看 name/email 是否都非空:
// git 还有一层自动推断(EMAIL 环境变量、主机名),推断得出来它就照常提交,那时不该弹框;
// 反过来只设了 name 没设 email 一样提交不了。判据就一个 —— git var 的退出码,
// 它和 git commit 走的是同一套 IDENT_STRICT,所以这个判断不会和 git 本人打架。
// 提交者身份是另一份(GIT_COMMITTER_* 环境变量可以只设一半),commit 两个都要,所以都问。
func (s *Service) identityOK(root string) bool {
	if _, err := s.run(root, nil, "var", "GIT_AUTHOR_IDENT"); err != nil {
		return false
	}
	_, err := s.run(root, nil, "var", "GIT_COMMITTER_IDENT")
	return err == nil
}

// checkIdentity 校验并规范化身份。挡三类输入:
//   - 空:git 会以 "empty ident name not allowed" 拒绝提交,不如这里就说清楚。
//   - 控制字符:换行会被 git 转义成 \n 原样写进 .git/config,读回来变成好几行,
//     那个配置文件从此看着像坏的。
//   - 尖括号:git 拼 "Name <email>" 时会把 <> 直接删掉(a<b>@c.d 存成 ab@c.d),
//     写进历史的和用户填的不是一个东西,不如让他改。
//
// 邮箱另外要求含 @ 且不带空格:git 两个都不检查,但提交历史里的邮箱是改不动的。
func checkIdentity(name, email string) (string, string, error) {
	n := strings.TrimSpace(name)
	e := strings.TrimSpace(email)
	if n == "" || e == "" || len(n) > identityMax || len(e) > identityMax {
		return "", "", errBadIdentity
	}
	for _, s := range []string{n, e} {
		for _, c := range s {
			if c < 0x20 || c == 0x7f || c == '<' || c == '>' {
				return "", "", errBadIdentity
			}
		}
	}
	if strings.Contains(e, " ") || !strings.Contains(e, "@") {
		return "", "", errBadIdentity
	}
	return n, e, nil
}
