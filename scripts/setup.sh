#!/usr/bin/env bash
# Diamond external machine bootstrap
# Usage: ./scripts/setup.sh [MAIN_MACHINE_IP]
set -euo pipefail

DIAMOND_PORT=7331
MAIN_IP="${1:-}"

echo "=== Diamond Setup ==="

# ---- Go ----
if ! command -v go &>/dev/null; then
  echo "[1/5] Installing Go 1.22..."
  curl -fsSL https://go.dev/dl/go1.22.4.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
  echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
  export PATH=$PATH:/usr/local/go/bin
else
  echo "[1/5] Go $(go version | awk '{print $3}') found"
fi

# ---- Ollama ----
if ! command -v ollama &>/dev/null; then
  echo "[2/5] Installing Ollama..."
  curl -fsSL https://ollama.ai/install.sh | sh
else
  echo "[2/5] Ollama found"
fi

# ---- Models ----
echo "[3/5] Pulling models (good for 8GB RAM)..."
ollama pull llama3.2:3b          # general chat + explanations (2GB)
ollama pull qwen2.5-coder:3b     # code-focused tasks (2GB)

# ---- Build Diamond ----
echo "[4/5] Building Diamond server..."
sudo mkdir -p /opt/diamond /var/lib/diamond
go build -o /tmp/diamond .
sudo cp /tmp/diamond /opt/diamond/diamond
sudo chmod +x /opt/diamond/diamond

# Create system user if missing
id -u diamond &>/dev/null || sudo useradd -r -s /bin/false diamond
sudo chown -R diamond:diamond /opt/diamond /var/lib/diamond

# ---- Systemd ----
echo "[5/5] Installing systemd service..."
sudo cp systemd/diamond.service /etc/systemd/system/diamond.service
sudo systemctl daemon-reload
sudo systemctl enable diamond
sudo systemctl restart diamond
sudo systemctl status diamond --no-pager

# ---- Firewall ----
if [ -n "$MAIN_IP" ]; then
  echo ""
  echo "Configuring UFW for main machine: $MAIN_IP"
  sudo ufw allow from "$MAIN_IP" to any port $DIAMOND_PORT comment "Diamond server"
  sudo ufw allow from "$MAIN_IP" to any port 11434 comment "Ollama direct access"
  # Block everything else on these ports
  sudo ufw deny in "$DIAMOND_PORT"
  sudo ufw deny in 11434
  sudo ufw --force enable
  echo "UFW rules applied."
fi

echo ""
echo "=== Done ==="
echo "Diamond: http://$(hostname -I | awk '{print $1}'):$DIAMOND_PORT/api/health"
echo "Log:     journalctl -u diamond -f"
