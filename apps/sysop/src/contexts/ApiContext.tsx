import { createContext } from 'react'
import { apiClient, type ApiClient } from '@/lib/api'

export const ApiContext = createContext<ApiClient>(apiClient)

export function ApiProvider({
  children,
  client = apiClient,
}: {
  children: React.ReactNode
  client?: ApiClient
}) {
  return <ApiContext.Provider value={client}>{children}</ApiContext.Provider>
}
