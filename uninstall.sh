#!/bin/sh
# Removes OpenCW, its root helper and its install record:
#
#   sudo /Applications/OpenCW.app/Contents/Resources/uninstall.sh
#
# Stopping the helper restores the network first.
if [ "$(/usr/bin/id -u)" -ne 0 ]; then
	echo "관리자 권한이 필요합니다. 앞에 sudo를 붙여 실행해주세요: sudo $0" >&2
	exit 1
fi
/bin/launchctl bootout system/io.github.0xmoomi.opencw 2>/dev/null
/bin/rm -f /Library/LaunchDaemons/io.github.0xmoomi.opencw.plist
/bin/rm -rf /Applications/OpenCW.app
/bin/rm -f /var/run/opencw.sock /var/run/opencw.lock
/usr/sbin/pkgutil --forget io.github.0xmoomi.opencw-mac >/dev/null 2>&1
echo "OpenCW를 삭제했습니다."
