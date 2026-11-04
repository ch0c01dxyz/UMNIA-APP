#!/usr/bin/env bash
# F1-02 — pasang RustFS Linux native (tanpa Docker) untuk UMNIA.
# Mengunduh rilis resmi, memverifikasi SHA256 dari SHA256SUMS resmi,
# memasang biner ke /usr/local/bin/rustfs, dan mendaftarkan systemd unit.
#
# Penggunaan (di host Linux):
#   RUSTFS_ACCESS_KEY='...' RUSTFS_SECRET_KEY='...' \
#     bash deploy/linux/install-rustfs.sh
#
# Kredensial TIDAK boleh di-hardcode dan TIDAK boleh di-commit.
# Versi dipatok di script ini; naikkan hanya setelah verifikasi rilis.
set -euo pipefail

RUSTFS_VERSION="${RUSTFS_VERSION:-1.0.4}"
: "${RUSTFS_ACCESS_KEY:?set RUSTFS_ACCESS_KEY}"
: "${RUSTFS_SECRET_KEY:?set RUSTFS_SECRET_KEY}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

BASE="https://github.com/rustfs/rustfs/releases/download/${RUSTFS_VERSION}"
# Unduh dengan nama file asli, karena SHA256SUMS merujuk nama itu;
# mengunduh sebagai rustfs.zip membuat sha256sum -c gagal "No such file".
curl -sL --fail -o SHA256SUMS "${BASE}/SHA256SUMS"
curl -sL --fail -o "rustfs-linux-x86_64-gnu-v${RUSTFS_VERSION}.zip" \
  "${BASE}/rustfs-linux-x86_64-gnu-v${RUSTFS_VERSION}.zip"
sha256sum -c <(grep "linux-x86_64-gnu-v${RUSTFS_VERSION}.zip" SHA256SUMS)

unzip -q "rustfs-linux-x86_64-gnu-v${RUSTFS_VERSION}.zip"
BIN="$(find . -name rustfs -type f | head -1)"
install -m 0755 "$BIN" /usr/local/bin/rustfs
/usr/local/bin/rustfs --version

mkdir -p /var/lib/rustfs /etc/default
cat > /etc/default/rustfs <<EOF
RUSTFS_ACCESS_KEY=${RUSTFS_ACCESS_KEY}
RUSTFS_SECRET_KEY=${RUSTFS_SECRET_KEY}
EOF
chmod 0600 /etc/default/rustfs

cat > /etc/systemd/system/rustfs.service <<'EOF'
[Unit]
Description=RustFS object storage (UMNIA)
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/default/rustfs
ExecStart=/usr/local/bin/rustfs server /var/lib/rustfs --address 127.0.0.1:9000
Restart=on-failure
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now rustfs
echo "RustFS siap di http://127.0.0.1:9000 (hanya localhost)."
