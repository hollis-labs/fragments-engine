import { useContext } from 'react'
import { ApiContext } from '@/contexts/ApiContext'

export function useApi() {
  return useContext(ApiContext)
}
