import TabsBase from '@hashicorp/react-tabs'
import s from '@hashicorp/nextjs-scripts/lib/providers/docs/style.module.css'
import EnterpriseAlertBase from '@hashicorp/react-enterprise-alert'

/**
 * ConfigEntryReference renders the reference docs for a config entry.
 * It creates two tabs, one for HCL docs and one for Kubernetes docs.
 *
 * @param {array<object>} keys  Array of objects, that describe all
 *                              keys that can be set for this config entry.
 * @param {boolean} topLevel    Indicates this is a reference block that contains
 *                              the top level keys vs a reference block that documents
 *                              nested keys and that is separated out for clarity.
 *
 * The objects in the keys array support the following keys:
 * - name <required>: the name of the HCL key, e.g. Name, Listener. This case sensitive.
 * - description <required>: the description of the key. If this key has different descriptions
 * for HCL vs. Kube YAML then description can be an object:
 * description: {
 *       hcl: 'HCL description',
 *       yaml: 'YAML description'
 *     }
 * - hcl <optional>: a boolean to indicate if this key should be shown in the HCL
 * documentation. Defaults to true.
 * - yaml <optional>: a boolean to indicate if this key should be shown in the YAML
 * documentation. Defaults to true.
 * - enterprise <optional>: a boolean to indicate if this key is Consul Enterprise
 * only. Defaults to false.
 * - children <optional>: accepts an array of keys that must be set under this key.
 * The schema for these keys is the same as the top level keys.
 * - type <optional>: the type and default of this key, e.g. string: "default".
 */
export default function ConfigEntryReference({ keys, topLevel = true }) {
  let kubeKeys = keys
  if (topLevel) {
    // Kube needs to have its non-top-level keys nested under a "spec" key.
    kubeKeys = toKubeKeys(keys)
  }

  return (
    <Tabs>
      <Tab heading="HCL">{renderKeys(keys, true)}</Tab>
      <Tab heading="Kubernetes YAML">{renderKeys(kubeKeys, false)}</Tab>
    </Tabs>
  )
}

/**
 * Renders keys as HTML. It works recursively through all keys.
 * @param {array} keys
 * @param {boolean} isHCLTab
 * @returns {JSX.Element|null}
 */
function renderKeys(keys, isHCLTab) {
  if (!keys) {
    return null
  }

  return (
    <ul>
      {keys.map((key) => {
        return renderKey(key, isHCLTab)
      })}
    </ul>
  )
}

/**
 * Renders a single key as its HTML element.
 *
 * @param {object} key
 * @param {boolea} isHCLTab
 * @returns {JSX.Element|null}
 */
function renderKey(key, isHCLTab) {
  let keyName = key.name
  if (!keyName) {
    return null
  }
  if (!isHCLTab) {
    keyName = toYAMLKeyName(keyName)
  }
  if (isHCLTab && key.hcl === false) {
    return null
  }
  if (!isHCLTab && key.yaml === false) {
    return null
  }

  let description = ''
  if (key.description) {
    if (typeof key.description === 'string') {
      description = key.description
    } else if (!isHCLTab && key.description.yaml) {
      description = key.description.yaml
    } else if (key.description.hcl) {
      description = key.description.hcl
    }
  }

  let htmlDescription = ''
  if (description !== '') {
    htmlDescription = markdownToHtml('- ' + description)
  }

  let type = ''
  if (key.type) {
    type = <code>{'(' + key.type + ')'}</code>
  }

  let enterpriseAlert = ''
  if (key.enterprise) {
    enterpriseAlert = <EnterpriseAlert inline />
  }

  const keyLower = keyName.toLowerCase()

  return (
    <li key={keyLower} className="g-type-long-body">
      <a id={keyLower} className="__target-lic" aria-hidden="" />
      <p>
        <a
          href={'#' + keyLower}
          aria-label={keyLower + ' permalink'}
          className="__permalink-lic"
        >
          <code>{keyName}</code>
        </a>{' '}
        {type}
        {enterpriseAlert}
        <span dangerouslySetInnerHTML={{ __html: htmlDescription }} />
      </p>
      {renderKeys(key.children, isHCLTab)}
    </li>
  )
}

/**
 * Constructs a keys object for Kubernetes out of HCL keys.
 * Really all this entails is nesting the correct keys under the Kubernetes
 * 'spec' key since in HCL there is no 'spec' key.
 *
 * @param {array} keys
 * @returns {array}
 */
function toKubeKeys(keys) {
  const topLevelKeys = keys.filter((key) => {
    return isTopLevelKubeKey(key.name)
  })
  const keysUnderSpec = keys.filter((key) => {
    return !isTopLevelKubeKey(key.name)
  })
  return topLevelKeys.concat([{ name: 'spec', children: keysUnderSpec }])
}

/**
 * Converts an HCL key name to a kube yaml key name.
 *
 * Examples:
 * - Protocol => protocol
 * - MeshGateway => meshGateway
 * - ACLToken => aclToken
 * - HTTP => http
 *
 * @param {string} hclKey
 * @returns {string}
 */
function toYAMLKeyName(hclKey) {
  // Handle something like HTTP.
  if (hclKey.toUpperCase() === hclKey) {
    return hclKey.toLowerCase()
  }

  let indexFirstLowercaseChar = 0
  for (let i = 0; i < hclKey.length; i++) {
    if (hclKey[i].toLowerCase() === hclKey[i]) {
      indexFirstLowercaseChar = i
      break
    }
  }

  // Special case to handle something like ACLToken => aclToken.
  if (indexFirstLowercaseChar > 1) {
    indexFirstLowercaseChar--
  }

  let yamlKey = ''
  for (let i = 0; i < indexFirstLowercaseChar; i++) {
    yamlKey += hclKey[i].toLowerCase()
  }
  yamlKey += hclKey.split('').slice(indexFirstLowercaseChar).join('')

  return yamlKey
}

/**
 * Converts a markdown string to its HTML representation.
 * Currently it only supports inline code blocks (e.g. `code here`) and
 * links (e.g. [link text](http://link-url) because these were the most
 * commonly used markdown features in the key descriptions.
 *
 * @param {string} markdown the input markdown
 * @returns {string}
 */
function markdownToHtml(markdown) {
  let html = markdown

  // Replace inline code blocks defined by backticks with <code></code>.
  while (html.indexOf('`') > 0) {
    html = html.replace('`', '<code>')
    if (html.indexOf('`') <= 0) {
      throw new Error(`'${markdown} does not have matching '\`' characters`)
    }
    html = html.replace('`', '</code>')
  }

  // Replace links, e.g. [link text](http://link-url),
  // with <a href="http://link-url">link text</a>.
  return html.replace(/\[(.*?)]\((.*?)\)/g, '<a href="$2">$1</a>')
}

/**
 * Returns true if key is a key used at the top level of a CRD. By top level we
 * mean not nested under any other key.
 *
 * @param {string} name name of the key
 *
 * @return {boolean}
 */
function isTopLevelKubeKey(name) {
  return (
    name.toLowerCase() === 'metadata' ||
    name.toLowerCase() === 'kind' ||
    name.toLowerCase() === 'apiversion'
  )
}

// Copied from https://github.com/hashicorp/nextjs-scripts/blob/04917da2191910d490182250d2828372aa1221c0/lib/providers/docs/index.jsx
// because there's no way to import it right now.
function Tabs({ children }) {
  if (!Array.isArray(children))
    throw new Error('Multiple <Tab> elements required')

  return (
    <span className={s.tabsRoot}>
      <TabsBase
        items={children.map((Block) => ({
          heading: Block.props.heading,
          // eslint-disable-next-line react/display-name
          tabChildren: () => Block,
        }))}
      />
    </span>
  )
}

// Copied from https://github.com/hashicorp/nextjs-scripts/blob/04917da2191910d490182250d2828372aa1221c0/lib/providers/docs/index.jsx
// because there's no way to import it right now.
function Tab({ children }) {
  return <>{children}</>
}

function EnterpriseAlert(props) {
  return <EnterpriseAlertBase product={'consul'} {...props} />
}