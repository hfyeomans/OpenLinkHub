#!/bin/sh

set -e
PRODUCT="OpenLinkHub"
PRODUCT_FILE="/opt/$PRODUCT/$PRODUCT"
SERVICE_USER="openlinkhub"

if [ ! -f $PRODUCT ]; then
  echo "No binary file. Exit"
  exit 0
fi

if [ -f $PRODUCT_FILE ]; then
  echo "Performing upgrade..."
  sudo systemctl stop $PRODUCT
  cp OpenLinkHub "/opt/$PRODUCT/"
  cp -r web/ "/opt/$PRODUCT/"
  cp -r static/ "/opt/$PRODUCT/"
  cp -r database/ "/opt/$PRODUCT/"
  sudo cp 99-openlinkhub.rules /etc/udev/rules.d/
  sudo udevadm control --reload-rules
  sudo udevadm trigger
  chown -R "$SERVICE_USER":"$SERVICE_USER" /opt/$PRODUCT
  find /opt/$PRODUCT -type d -exec chmod 750 {} +
  find /opt/$PRODUCT -type f -exec chmod 640 {} +
  chmod 750 /opt/$PRODUCT/$PRODUCT
  chmod 700 /opt/$PRODUCT/database
  sudo systemctl start $PRODUCT
  exit 0
fi