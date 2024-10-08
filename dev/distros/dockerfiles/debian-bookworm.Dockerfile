FROM debian:bookworm-slim

# Install necessary packages
RUN apt-get update && apt-get install -y \
  sudo \
  systemd \
  openssh-server \
  ca-certificates \
  bash \
  coreutils \
  curl \
  inotify-tools \
  ipvsadm \
  kmod \
  iproute2 \
  systemd-timesyncd \
  expect \
  vim

# Override timesyncd config to allow it to run in containers
COPY ./timesyncd-override.conf /etc/systemd/system/systemd-timesyncd.service.d/override.conf

# Disable getty service as it's flaky and doesn't apply in containers
RUN systemctl mask getty@tty1.service

# Export kube config
ENV KUBECONFIG=/var/lib/k0s/pki/admin.conf

CMD ["/sbin/init"]
