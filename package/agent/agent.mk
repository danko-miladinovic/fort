################################################################################
#
# Fort Agent
#
################################################################################

AGENT_VERSION = main

define AGENT_BUILD_CMDS
	echo "Agent build command called"
endef

$(eval $(generic-package))