import { createApiContext } from '@hollis-labs/sysop-ui'
import { apiClient } from '@/lib/api'

/** App API context, built from the kit factory with the concrete Fragments client. */
export const { ApiProvider, useApi } = createApiContext(apiClient)
