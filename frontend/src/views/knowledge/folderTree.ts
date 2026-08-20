import type { KnowledgeFolderNode, KnowledgeFolderTree } from '@/api/knowledge-base/index'

/**
 * Pure helpers behind the knowledge base folder sidebar.
 *
 * A folder selection is just a path, where the empty string is the knowledge
 * base root. The root is a real node of the tree — every top-level folder is
 * its child — so there is no separate "all documents" pseudo-row: what the root
 * row lists is decided by the same recursive switch as any other folder.
 */
export const ROOT_FOLDER_PATH = ''

/** Keep in sync with types.MaxKnowledgeFolderDepth on the server. */
export const MAX_FOLDER_DEPTH = 16
/** Keep in sync with types.MaxKnowledgeFolderSegmentLength on the server. */
export const MAX_FOLDER_SEGMENT_LENGTH = 128
/** Keep in sync with types.MaxKnowledgeFolderPathLength on the server. */
export const MAX_FOLDER_PATH_LENGTH = 1024

const utf8Encoder = new TextEncoder()

/** 按 UTF-8 字节截断，并确保不会截断多字节字符。 */
function truncateUTF8Bytes(value: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  if (utf8Encoder.encode(value).byteLength <= maxBytes) return value

  let result = ''
  let usedBytes = 0
  for (const character of value) {
    const characterBytes = utf8Encoder.encode(character).byteLength
    if (usedBytes + characterBytes > maxBytes) break
    result += character
    usedBytes += characterBytes
  }
  return result
}

function utf8ByteLength(value: string): number {
  return utf8Encoder.encode(value).byteLength
}

/** Minimal shape of the browser File objects the upload flow deals with. */
export type UploadFileLike = {
  name: string
  webkitRelativePath?: string
}

/**
 * One rendered row of the flattened tree. The root row carries the knowledge
 * base totals; the component supplies its label.
 */
export type FolderRow = {
  kind: 'root' | 'folder'
  path: string
  name: string
  depth: number
  /** Documents stored directly in this folder. */
  documentCount: number
  /** Documents in this folder plus every descendant folder. */
  totalCount: number
  hasChildren: boolean
}

/**
 * A browser only sets webkitRelativePath when the file came from a directory
 * picker or a dropped directory, which is exactly what distinguishes a folder
 * upload from picking individual files.
 */
export function isFolderUpload(file: UploadFileLike): boolean {
  return !!file.webkitRelativePath
}

/**
 * Build the path-qualified `fileName` form field that the backend splits into
 * folder_path + file_name. Two sources are combined: the destination folder
 * chosen for the batch and the browser's webkitRelativePath, whose picked
 * directory becomes a folder under that destination.
 *
 * Returns undefined for a plain single-file upload into the root so that request
 * stays byte-identical to the pre-folder behaviour.
 */
export function buildUploadFileName(
  file: UploadFileLike,
  targetFolder: string,
): string | undefined {
  const segments: string[] = []
  if (targetFolder) segments.push(targetFolder)
  if (file.webkitRelativePath) {
    segments.push(...file.webkitRelativePath.split('/').filter(Boolean).slice(0, -1))
  }
  if (segments.length === 0) return undefined
  return `${segments.join('/')}/${file.name}`
}

/**
 * Breadcrumb entries for a folder path, from the top level down to the leaf.
 * The root has no crumbs of its own — callers render it as the leading item.
 */
export function folderBreadcrumbs(path: string): Array<{ name: string; path: string }> {
  if (!path) return []
  const segments = path.split('/')
  return segments.map((name, index) => ({
    name,
    path: segments.slice(0, index + 1).join('/'),
  }))
}

/**
 * The root, the path itself and every folder in between, used to keep the
 * selected folder reachable by expanding the rows above it.
 */
export function folderAncestorPaths(path: string): string[] {
  const paths = [ROOT_FOLDER_PATH]
  if (!path) return paths
  const segments = path.split('/')
  segments.forEach((_, index) => paths.push(segments.slice(0, index + 1).join('/')))
  return paths
}

/** Depth-first search for a folder path in the tree. The root always exists. */
export function folderPathExists(folders: KnowledgeFolderNode[], path: string): boolean {
  if (path === ROOT_FOLDER_PATH) return true
  return folders.some((node) => node.path === path || folderPathExists(node.children || [], path))
}

function flattenFolders(
  folders: KnowledgeFolderNode[],
  expanded: Set<string>,
  depth: number,
): FolderRow[] {
  const rows: FolderRow[] = []
  folders.forEach((node) => {
    const hasChildren = !!node.children?.length
    rows.push({
      kind: 'folder',
      path: node.path,
      name: node.name,
      depth,
      documentCount: node.document_count,
      totalCount: node.total_count,
      hasChildren,
    })
    if (hasChildren && expanded.has(node.path)) {
      rows.push(...flattenFolders(node.children!, expanded, depth + 1))
    }
  })
  return rows
}

/**
 * Flatten the tree into the visible rows: the root first, then the folders
 * nested beneath it, honouring the expanded set at every level.
 */
export function buildFolderRows(
  tree: KnowledgeFolderTree | null,
  expanded: Set<string>,
): FolderRow[] {
  const folders = tree?.folders ?? []
  const root: FolderRow = {
    kind: 'root',
    path: ROOT_FOLDER_PATH,
    name: '',
    depth: 0,
    documentCount: tree?.root_document_count ?? 0,
    totalCount: tree?.total_document_count ?? 0,
    hasChildren: folders.length > 0,
  }
  if (!root.hasChildren || !expanded.has(ROOT_FOLDER_PATH)) return [root]
  return [root, ...flattenFolders(folders, expanded, 1)]
}

/**
 * Direct sub-folders of a folder, i.e. what the document list shows as folder
 * entries while browsing it. The root's children are the top-level folders.
 */
export function childFolders(
  tree: KnowledgeFolderTree | null,
  path: string,
): KnowledgeFolderNode[] {
  const folders = tree?.folders ?? []
  if (path === ROOT_FOLDER_PATH) return folders
  const find = (nodes: KnowledgeFolderNode[]): KnowledgeFolderNode | undefined => {
    for (const node of nodes) {
      if (node.path === path) return node
      const found = find(node.children || [])
      if (found) return found
    }
    return undefined
  }
  return find(folders)?.children ?? []
}

/**
 * Whether the document list is filtering rather than browsing.
 *
 * This is the single input that decides how a folder selection is read, and it
 * comes from what the user is already doing instead of from a mode switch they
 * have to understand:
 *
 * - Browsing (no filter active) lists the selected folder's own contents — its
 *   sub-folders as entries plus the documents directly inside it, the way a file
 *   manager does. A document uploaded on its own therefore sits at the top level
 *   next to the folders, which is exactly where it belongs.
 * - Filtering searches the selected folder and everything below it, returning a
 *   flat result set, because a search that stopped at one level would hide
 *   matches for no good reason.
 */
export function isFilteringDocuments(filters: {
  keyword?: string
  tagIds?: string[]
  fileType?: string
  parseStatus?: string
  source?: string
  timeRange?: string[]
}): boolean {
  return !!(
    filters.keyword?.trim() ||
    filters.tagIds?.length ||
    filters.fileType ||
    filters.parseStatus ||
    filters.source ||
    filters.timeRange?.filter(Boolean).length
  )
}

/**
 * Canonicalize a user-typed folder path the same way the server does, so the
 * move dialog can preview and compare the destination it is about to send.
 */
export function normalizeFolderPath(path: string): string {
  const segments: string[] = []
  for (const raw of path.replace(/\\/g, '/').split('/')) {
    let segment = raw.trim().replace(/[. ]+$/, '')
    if (!segment || segment === '.' || segment === '..') continue
    if (utf8ByteLength(segment) > MAX_FOLDER_SEGMENT_LENGTH) {
      segment = truncateUTF8Bytes(segment, MAX_FOLDER_SEGMENT_LENGTH).trim()
    }
    if (!segment) continue
    segments.push(segment)
    if (segments.length >= MAX_FOLDER_DEPTH) break
  }
  let normalized = segments.join('/')
  while (utf8ByteLength(normalized) > MAX_FOLDER_PATH_LENGTH && segments.length > 0) {
    segments.pop()
    normalized = segments.join('/')
  }
  return normalized
}

/** Destination path for a new sub-folder of `parent`. */
export function joinFolderPath(parent: string, name: string): string {
  return normalizeFolderPath(parent ? `${parent}/${name}` : name)
}

/** 向目录树中插入一个目录及缺失的祖先目录，返回不可变的新树。 */
export function addFolderToTree(
  tree: KnowledgeFolderTree | null,
  path: string,
): KnowledgeFolderTree {
  const next: KnowledgeFolderTree = tree
    ? {
        ...tree,
        folders: tree.folders.map((folder) => cloneFolderNode(folder)),
      }
    : {
        root_document_count: 0,
        total_document_count: 0,
        folders: [],
      }
  const segments = normalizeFolderPath(path).split('/').filter(Boolean)
  let nodes = next.folders
  let currentPath = ''

  segments.forEach((segment) => {
    currentPath = currentPath ? `${currentPath}/${segment}` : segment
    let node = nodes.find((candidate) => candidate.path === currentPath)
    if (!node) {
      node = {
        path: currentPath,
        name: segment,
        document_count: 0,
        total_count: 0,
        children: [],
      }
      nodes.push(node)
    }
    if (!node.children) node.children = []
    nodes = node.children
  })
  return next
}

/** 从目录树中移除一个目录及其子树，返回不可变的新树。 */
export function removeFolderFromTree(
  tree: KnowledgeFolderTree | null,
  path: string,
): KnowledgeFolderTree | null {
  if (!tree) return null
  const normalizedPath = normalizeFolderPath(path)
  const remove = (nodes: KnowledgeFolderNode[]): KnowledgeFolderNode[] =>
    nodes
      .filter((node) => node.path !== normalizedPath)
      .map((node) => ({
        ...node,
        children: node.children ? remove(node.children) : node.children,
      }))
  return { ...tree, folders: remove(tree.folders) }
}

/** 回滚本次乐观创建仍为空的目录，不影响其他并发创建的子目录。 */
export function rollbackFolderCreation(
  tree: KnowledgeFolderTree | null,
  createdPaths: string[],
): KnowledgeFolderTree | null {
  let next = tree
  const paths = [...new Set(createdPaths)].sort((a, b) => b.split('/').length - a.split('/').length)
  paths.forEach((path) => {
    if (!next) return
    const find = (nodes: KnowledgeFolderNode[]): KnowledgeFolderNode | undefined => {
      for (const node of nodes) {
        if (node.path === path) return node
        const child = find(node.children || [])
        if (child) return child
      }
      return undefined
    }
    const node = find(next.folders)
    if (!node || node.document_count > 0 || node.total_count > 0 || node.children?.length) return
    next = removeFolderFromTree(next, path)
  })
  return next
}

function cloneFolderNode(node: KnowledgeFolderNode): KnowledgeFolderNode {
  return {
    ...node,
    children: node.children?.map((child) => cloneFolderNode(child)),
  }
}

/** Flat picker row for a canonical folder path (used for optimistic create rows). */
export function folderOptionFromPath(path: string): { path: string; name: string; depth: number } {
  const segments = path.split('/').filter(Boolean)
  return {
    path,
    name: segments[segments.length - 1] || path,
    depth: Math.max(segments.length - 1, 0),
  }
}

/** Sort folder picker rows in tree order (parent before child, siblings lexicographic). */
export function sortFolderOptions<T extends { path: string }>(options: T[]): T[] {
  return [...options].sort((a, b) => {
    const aParts = a.path.split('/').filter(Boolean)
    const bParts = b.path.split('/').filter(Boolean)
    const max = Math.max(aParts.length, bParts.length)
    for (let i = 0; i < max; i += 1) {
      const aSeg = aParts[i]
      const bSeg = bParts[i]
      if (aSeg === undefined) return -1
      if (bSeg === undefined) return 1
      const cmp = aSeg.localeCompare(bSeg)
      if (cmp !== 0) return cmp
    }
    return 0
  })
}

/**
 * Whether a folder can be renamed/moved to `to`: a folder cannot land inside its
 * own subtree, which would make that subtree unreachable.
 */
export function canMoveFolderTo(from: string, to: string): boolean {
  const source = normalizeFolderPath(from)
  const target = normalizeFolderPath(to)
  if (!source || !target) return false
  if (target === source) return false
  return !target.startsWith(`${source}/`)
}
