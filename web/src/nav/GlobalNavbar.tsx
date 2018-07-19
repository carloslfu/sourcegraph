import * as H from 'history'
import * as React from 'react'
import { Link } from 'react-router-dom'
import { Subscription } from 'rxjs'
import { authRequired } from '../auth'
import { ExtensionsChangeProps, ExtensionsProps } from '../backend/features'
import * as GQL from '../backend/graphqlschema'
import { CXPControllerProps, CXPEnvironmentProps } from '../cxp/CXPEnvironment'
import { parseSearchURLQuery, SearchOptions } from '../search'
import { SearchNavbarItem } from '../search/input/SearchNavbarItem'
import { NavLinks } from './NavLinks'

interface Props extends ExtensionsProps, ExtensionsChangeProps, CXPEnvironmentProps, CXPControllerProps {
    history: H.History
    location: H.Location
    user: GQL.IUser | null
    isLightTheme: boolean
    onThemeChange: () => void
    navbarSearchQuery: string
    onNavbarQueryChange: (query: string) => void
    showHelpPopover: boolean
    onHelpPopoverToggle: (visible?: boolean) => void

    /**
     * Whether to use the low-profile form of the navbar, which has no border or background. Used on the search
     * homepage.
     */
    lowProfile: boolean
}

interface State {
    authRequired?: boolean
}

export class GlobalNavbar extends React.PureComponent<Props, State> {
    public state: State = {}

    private subscriptions = new Subscription()

    constructor(props: Props) {
        super(props)

        /**
         * Reads initial state from the props (i.e. URL parameters).
         */
        const options = parseSearchURLQuery(props.location.search || '')
        if (options) {
            props.onNavbarQueryChange(options.query)
        } else {
            // If we have no component state, then we may have gotten unmounted during a route change.
            const state: SearchOptions | undefined = props.location.state
            props.onNavbarQueryChange(state ? state.query : '')
        }
    }

    public componentDidMount(): void {
        this.subscriptions.add(authRequired.subscribe(authRequired => this.setState({ authRequired })))
    }

    public componentWillUnmount(): void {
        this.subscriptions.unsubscribe()
    }

    public render(): JSX.Element | null {
        const logo = <img className="global-navbar__logo" src="/.assets/img/sourcegraph-mark.svg" />
        return (
            <div className={`global-navbar ${this.props.lowProfile ? '' : 'global-navbar--bg'}`}>
                {this.props.lowProfile ? (
                    <div />
                ) : (
                    <>
                        <div className="global-navbar__left">
                            {this.state.authRequired ? (
                                <div className="global-navbar__logo-link">{logo}</div>
                            ) : (
                                <Link to="/search" className="global-navbar__logo-link">
                                    {logo}
                                </Link>
                            )}
                        </div>
                        {!this.state.authRequired && (
                            <div className="global-navbar__search-box-container">
                                <SearchNavbarItem
                                    {...this.props}
                                    navbarSearchQuery={this.props.navbarSearchQuery}
                                    onChange={this.props.onNavbarQueryChange}
                                />
                            </div>
                        )}
                    </>
                )}
                {!this.state.authRequired && (
                    <NavLinks {...this.props} className="global-navbar__nav-links" adjacentToQueryInput={true} />
                )}
            </div>
        )
    }
}
