/*
 * Copyright 2017-2018 Dgraph Labs, Inc. and Contributors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package query

import (
	"github.com/dgraph-io/dgraph/types"
	"github.com/dgraph-io/dgraph/x"
	"github.com/golang/glog"
)

type mathTree struct {
	Fn    string
	Var   string
	Const types.Val // If its a const value node.
	Val   map[uint64]types.Val
	Child []*mathTree
}

// processBinary handles the binary operands like
// +, -, *, /, %, logbase
func processBinary(mNode *mathTree) error {
	destMap := make(map[uint64]types.Val)
	aggName := mNode.Fn

	mpl := mNode.Child[0].Val
	mpr := mNode.Child[1].Val
	cl := mNode.Child[0].Const
	cr := mNode.Child[1].Const

	f := func(k uint64) error {
		ag := aggregator{
			name: aggName,
		}
		lVal := mpl[k]
		if cl.Value != nil {
			// Use the constant value that was supplied.
			lVal = cl
		}
		rVal := mpr[k]
		if cr.Value != nil {
			// Use the constant value that was supplied.
			rVal = cr
		}
		err := ag.ApplyVal(lVal)
		if err != nil {
			return err
		}
		err = ag.ApplyVal(rVal)
		if err != nil {
			return err
		}
		destMap[k], err = ag.Value()
		if err != nil {
			return err
		}
		return nil
	}

	if len(mpl) != 0 || len(mpr) != 0 {
		for k := range mpr {
			if err := f(k); err != nil {
				return err
			}
		}
		for k := range mpl {
			if _, ok := mpr[k]; ok {
				continue
			}
			if err := f(k); err != nil {
				return err
			}
		}
		mNode.Val = destMap
		return nil
	}

	if cl.Value != nil && cr.Value != nil {
		// Both maps are nil, so 2 constatns.
		ag := aggregator{
			name: aggName,
		}
		err := ag.ApplyVal(cl)
		if err != nil {
			return err
		}
		err = ag.ApplyVal(cr)
		if err != nil {
			return err
		}
		mNode.Const, err = ag.Value()
		return err
	}
	return nil
}

// processUnary handles the unary operands like
// u-, log, exp, since, floor, ceil
func processUnary(mNode *mathTree) error {
	destMap := make(map[uint64]types.Val)
	srcMap := mNode.Child[0].Val
	aggName := mNode.Fn
	ch := mNode.Child[0]
	ag := aggregator{
		name: aggName,
	}
	if ch.Const.Value != nil {
		// Use the constant value that was supplied.
		err := ag.ApplyVal(ch.Const)
		if err != nil {
			return err
		}
		mNode.Const, err = ag.Value()
		return err
	}

	for k, val := range srcMap {
		err := ag.ApplyVal(val)
		if err != nil {
			return err
		}
		destMap[k], err = ag.Value()
		if err != nil {
			return err
		}
	}
	mNode.Val = destMap
	return nil

}

// processBinaryBoolean handles the binary operands which
// return a boolean value.
// All the inequality operators (<, >, <=, >=, !=, ==)
func processBinaryBoolean(mNode *mathTree) error {
	destMap := make(map[uint64]types.Val)
	srcMap := mNode.Child[0].Val
	aggName := mNode.Fn

	ch := mNode.Child[1]
	curMap := ch.Val
	for k, val := range srcMap {
		curVal := curMap[k]
		if ch.Const.Value != nil {
			// Use the constant value that was supplied.
			curVal = ch.Const
		}
		res, err := compareValues(aggName, val, curVal)
		if err != nil {
			return x.Wrapf(err, "Wrong values in comaprison function.")
		}
		destMap[k] = types.Val{
			Tid:   types.BoolID,
			Value: res,
		}
	}
	mNode.Val = destMap
	return nil
}

// processTernary handles the ternary operand cond()
func processTernary(mNode *mathTree) error {
	destMap := make(map[uint64]types.Val)
	aggName := mNode.Fn
	condMap := mNode.Child[0].Val
	if len(condMap) == 0 {
		return x.Errorf("Expected a value variable in %v but missing.", aggName)
	}
	varOne := mNode.Child[1].Val
	varTwo := mNode.Child[2].Val
	constOne := mNode.Child[1].Const
	constTwo := mNode.Child[2].Const
	for k, val := range condMap {
		var res types.Val
		v, ok := val.Value.(bool)
		if !ok {
			return x.Errorf("First variable of conditional function not a bool value")
		}
		if v {
			// Pick the value of first map.
			if constOne.Value != nil {
				res = constOne
			} else {
				res = varOne[k]
			}
		} else {
			// Pick the value of second map.
			if constTwo.Value != nil {
				res = constTwo
			} else {
				res = varTwo[k]
			}
		}
		destMap[k] = res
	}
	mNode.Val = destMap
	return nil
}

func processNary(mNode *mathTree) error {
	destMap := make(map[uint64]types.Val)
	aggMap := make(map[uint64]*aggregator)
	aggName := mNode.Fn

	f := func(k uint64, val map[uint64]types.Val, constant types.Val) error {
		if _, ok := aggMap[k]; !ok {
			aggMap[k] = &aggregator{
				name: aggName,
			}
		}
		agg := aggMap[k]

		lVal := val[k]
		if constant.Value != nil {
			// Use the constant value that was supplied.
			lVal = constant
		}

		return agg.ApplyVal(lVal)
	}

	constantAgg := aggregator{
		name: aggName,
	}

	allConsts := true
	for i := 0; i < len(mNode.Child); i++ {
		val := mNode.Child[i].Val
		constant := mNode.Child[i].Const

		if len(val) > 0 {
			allConsts = false
			for k := range val {
				if err := f(k, val, constant); err != nil {
					return err
				}
			}
		}

		if constant.Value != nil {
			if err := constantAgg.ApplyVal(constant); err != nil {
				return err
			}
		}
	}

	if allConsts {
		val, err := constantAgg.Value()
		mNode.Const = val
		return err
	}

	for k, agg := range aggMap {
		val, err := agg.Value()
		destMap[k] = val
		if err != nil {
			return err
		}
	}
	mNode.Val = destMap

	return nil
}

func evalMathTree(mNode *mathTree) error {
	if mNode.Const.Value != nil {
		return nil
	}
	if mNode.Var != "" {
		if len(mNode.Val) == 0 {
			glog.V(2).Infof("Variable %v not yet populated or missing.", mNode.Var)
		}
		// This is a leaf node whose value is already populated. So return.
		return nil
	}

	for _, child := range mNode.Child {
		// Process the child nodes first.
		err := evalMathTree(child)
		if err != nil {
			return err
		}
	}

	aggName := mNode.Fn
	if isUnary(aggName) {
		if len(mNode.Child) != 1 {
			return x.Errorf("Function %v expects 1 argument. But got: %v", aggName,
				len(mNode.Child))
		}
		return processUnary(mNode)
	}

	if isBinary(aggName) {
		if len(mNode.Child) != 2 {
			return x.Errorf("Function %v expects 2 argument. But got: %v", aggName,
				len(mNode.Child))
		}
		return processBinary(mNode)
	}

	if isBinaryBoolean(aggName) {
		if len(mNode.Child) != 2 {
			return x.Errorf("Function %v expects 2 argument. But got: %v", aggName,
				len(mNode.Child))
		}
		return processBinaryBoolean(mNode)
	}

	if isTernary(aggName) {
		if len(mNode.Child) != 3 {
			return x.Errorf("Function %v expects 3 arguments. But got: %v", aggName,
				len(mNode.Child))
		}
		return processTernary(mNode)
	}

	if isNary(aggName) {
		if len(mNode.Child) == 0 {
			return x.Errorf("Function %v expects at least one argument. But got: %v", aggName,
				len(mNode.Child))
		}
		return processNary(mNode)
	}

	return x.Errorf("Unhandled Math operator: %v", aggName)
}
