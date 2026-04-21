################################################################################
#
# Fort Client
#
################################################################################

FORT_CLIENT_VERSION = main
FORT_CLIENT_SITE = $(BR2_EXTERNAL_FORT_PATH)/..
FORT_CLIENT_SITE_METHOD = local
FORT_CLIENT_GOMOD = github.com/danko-miladinovic/fort/client
FORT_CLIENT_SUBDIR = client
FORT_CLIENT_BIN_NAME = client

define FORT_CLIENT_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/bin/client $(TARGET_DIR)/usr/bin/client
	$(INSTALL) -D -m 0644 $(BR2_EXTERNAL_FORT_PATH)/package/fort-client/fort-client.service \
		$(TARGET_DIR)/usr/lib/systemd/system/fort-client.service
	mkdir -p $(TARGET_DIR)/etc/systemd/system/multi-user.target.wants
	ln -sf /usr/lib/systemd/system/fort-client.service \
		$(TARGET_DIR)/etc/systemd/system/multi-user.target.wants/fort-client.service
endef

$(eval $(golang-package))
