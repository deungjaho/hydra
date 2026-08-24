# Hydra AUR 推送规范

## AUR 包信息

- **包名**: `hydra-proxy`
- **AUR 维护者**: `deungjaho`
- **AUR 页面**: https://aur.archlinux.org/packages/hydra-proxy
- **SSH 推送**: `aur@aur.archlinux.org:hydra-proxy.git`（通过 7890 代理）

## 文件结构

```
hydra/aur/
├── PKGBUILD       # AUR PKGBUILD 模板（版本号需更新）
├── .SRCINFO       # makepkg --printsrcinfo 生成（推送前必须更新）
└── README.md      # 本文件
```

## 推送流程

### 前提

- Mac 上有 AUR SSH key: `~/.ssh/aur/id_ed25519_aur`
- Mac 通过 7890 代理连 AUR: `ssh -o ProxyCommand="nc -x 127.0.0.1:7890 %h %p" aur@aur.archlinux.org`
- hydra 源码已 push 到 GitHub
- hydra 源码有对应的 git tag（`v0.8.0` 等）

### 步骤

```bash
# 1. 在 hydra 源码打 tag 并 push
cd ~/Work/olympus/hydra
git tag v0.8.0
git push origin v0.8.0

# 2. 更新 PKGBUILD 版本号
cd aur/
# 编辑 PKGBUILD，改 pkgver=0.8.0 pkgrel=1

# 3. 生成 .SRCINFO（需要在 Arch Linux 上执行，或用 docker）
# 在 omarchy 上：
scp PKGBUILD omarchy:/tmp/hydra-pkgbuild/
ssh omarchy 'cd /tmp/hydra-pkgbuild && makepkg --printsrcinfo > .SRCINFO'
scp omarchy:/tmp/hydra-pkgbuild/.SRCINFO .

# 4. clone AUR repo（首次或更新）
# 在 Mac 上（通过代理）：
GIT_SSH_COMMAND='ssh -i ~/.ssh/aur/id_ed25519_aur -o IdentitiesOnly=yes -o ProxyCommand="nc -x 127.0.0.1:7890 %h %p"' \
  git clone aur@aur.archlinux.org:hydra-proxy.git /tmp/hydra-aur

# 5. 复制 PKGBUILD 和 .SRCINFO 到 AUR clone
cp PKGBUILD .SRCINFO /tmp/hydra-aur/

# 6. 提交并推送
cd /tmp/hydra-aur
git add PKGBUILD .SRCINFO
git commit -m "update to 0.8.0"
GIT_SSH_COMMAND='ssh -i ~/.ssh/aur/id_ed25519_aur -o IdentitiesOnly=yes -o ProxyCommand="nc -x 127.0.0.1:7890 %h %p"' \
  git push origin master

# 7. 验证
curl -s "https://aur.archlinux.org/rpc/?v=5&type=info&arg=hydra-proxy" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d['results'][0]; print(r['Version'])"
```

### omarchy 上更新

```bash
yay -S hydra-proxy
# 或
yay -S hydra-proxy --overwrite '/usr/bin/hydra'
systemctl --user restart hydra.service
```

## 版本规范

- `pkgver` 跟 hydra git tag 一致（`v0.8.0` → `pkgver=0.8.0`）
- `pkgrel` 每次只改 PKGBUILD 不改源码时 +1（`0.8.0-1` → `0.8.0-2`）
- 新 tag 时 `pkgrel` 重置为 1

## 注意

- PKGBUILD 的 `source` 指向 GitHub tag tarball，所以**必须先打 tag 再推 AUR**
- `sha256sums=(SKIP)` 当前跳过校验。如果要严格校验，先下载 tarball 算 sha256 再填
- AUR 只接受 `master` 分支推送
- AUR 不接受 binary，只接受 PKGBUILD + .SRCINFO
