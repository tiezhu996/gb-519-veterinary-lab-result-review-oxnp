
import { request } from './client';
import type { DomainRecord, SplitSpecimenInput } from '../types/domain';

export async function listSpecimen(page = 1, pageSize = 20, search = '') {
  return request<DomainRecord[]>(`/specimens?page=${page}&pageSize=${pageSize}&search=${encodeURIComponent(search)}`);
}
export async function createSpecimen(input: Partial<DomainRecord>) {
  return request<DomainRecord>('/specimens', { method: 'POST', body: JSON.stringify(input) });
}
export async function transitionSpecimen(id: number, status: string, expectedVersion: number, reason: string) {
  return request<DomainRecord>(`/specimens/${id}/transition`, {
    method: 'POST', body: JSON.stringify({ status, expectedVersion, reason }),
  });
}
export async function splitSpecimen(id: number, input: SplitSpecimenInput) {
  return request<DomainRecord>(`/specimens/${id}/split`, {
    method: 'POST', body: JSON.stringify(input),
  });
}
